package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"time"

	worker "axon-worker"
	"axon-worker/llm"
	"axon-worker/tools"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))
	ctx := context.Background()

	// Parse command-line flags for the queue and engine service URLs
	queueURL := flag.String("queue", "http://localhost:8080", "Queue service URL")
	engineURL := flag.String("engine", "http://localhost:8000", "Engine service URL")
	groqModel := flag.String("model", "openai/gpt-oss-120b", "Groq model to use for llm_call steps")
	flag.Parse()

	httpClient := &http.Client{Timeout: 10 * time.Second}

	toolRegistry := map[string]tools.Tool{
		"echo": tools.Echo{},
	}
	if apiKey := os.Getenv("TAVILY_API_KEY"); apiKey != "" {
		toolRegistry["tavily_search"] = tools.NewTavilySearch(apiKey, httpClient)
	} else {
		logger.Warn("TAVILY_API_KEY not set: tavily_search tool_call steps will be nacked until it is provided")
	}

	runners := map[string]StepRunner{
		worker.JobTypeToolCall: newToolCallRunner(toolRegistry),
	}

	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		groqClient := llm.NewGroqClient(apiKey, *groqModel, httpClient)
		runners[worker.JobTypeLLMCall] = newLLMRunner(groqClient)
		// supervisor steps use Decide, not Complete: the model is
		// constrained (via Groq's JSON mode) to answer with one of
		// payload.Options or "done", instead of free text the engine
		// then has to exact-match against unaided (#39's finding:
		// prompt-only constraint works but has no structural guarantee).
		runners[worker.JobTypeSupervisor] = newSupervisorRunner(groqClient)
	} else {
		logger.Warn("GROQ_API_KEY not set: llm_call and supervisor jobs will be nacked until it is provided")
	}

	for {
		pollOnce(ctx, httpClient, *queueURL, *engineURL, runners, logger)
	}

}

func pollOnce(ctx context.Context, httpClient *http.Client, queueURL, engineURL string, runners map[string]StepRunner, logger *slog.Logger) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queueURL+"/jobs/next", nil)
	if err != nil {
		logger.Error("failed to build request", "error", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		logger.Error("failed to poll queue", "error", err)
		time.Sleep(time.Second)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		time.Sleep(time.Second) // nothing pending, wait before polling again
		return
	}

	if resp.StatusCode != http.StatusOK {
		logger.Error("unexpected status from queue", "status", resp.StatusCode)
		time.Sleep(time.Second)
		return
	}

	var job worker.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		logger.Error("failed to decode job", "error", err)
		return
	}

	var payload worker.StepPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		logger.Error("failed to unmarshal job payload", "job_id", job.ID, "error", err)
		return
	}

	// Every log line from here on carries run_id/step_id, so a run's
	// activity can be grepped out of the worker's logs the same way as
	// the engine's (see #38).
	log := logger.With("run_id", payload.RunID, "step_id", payload.StepID, "job_id", job.ID)
	log.Info("dequeued job", "job_type", job.Type)

	runner, ok := runners[job.Type]
	if !ok {
		reason := fmt.Sprintf("unsupported job type: %s", job.Type)
		log.Error("failed to run step", "reason", reason)
		nackJob(ctx, httpClient, queueURL, engineURL, job.ID, payload, reason, log)
		return
	}

	output, err := runner(ctx, payload)
	if err != nil {
		log.Error("failed to run step", "error", err)
		nackJob(ctx, httpClient, queueURL, engineURL, job.ID, payload, err.Error(), log)
		return
	}

	// Ack the job with the queue
	ackReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queueURL+"/jobs/"+job.ID+"/ack", nil)
	if err != nil {
		log.Error("failed to build ack request", "error", err)
		return
	}
	ackResp, err := httpClient.Do(ackReq)
	if err != nil {
		log.Error("failed to ack job", "error", err)
		return
	}
	_ = ackResp.Body.Close()

	// Notify the engine that the step completed
	webhookPayload := worker.WebhookPayload{
		RunID:  payload.RunID,
		StepID: payload.StepID,
		Output: output,
	}
	webhookJSON, err := json.Marshal(webhookPayload)
	if err != nil {
		log.Error("failed to marshal webhook payload", "error", err)
		return
	}

	webhookReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL+"/webhook/complete", bytes.NewReader(webhookJSON))
	if err != nil {
		log.Error("failed to build webhook request", "error", err)
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookResp, err := httpClient.Do(webhookReq)
	if err != nil {
		log.Error("failed to notify engine", "error", err)
		return
	}
	_ = webhookResp.Body.Close()

	log.Info("processed step")
}

// nackJob marks a job as failed with the queue, which either retries
// it (see #37) or, once retries are exhausted, permanently fails it.
// In the permanent case the queue's response says so, and this tells
// the engine the step is never coming back (see #47) - otherwise the
// run would just sit at "in_progress" forever with no indication
// anything went wrong.
func nackJob(ctx context.Context, httpClient *http.Client, queueURL, engineURL, jobID string, payload worker.StepPayload, reason string, log *slog.Logger) {
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		log.Error("failed to marshal nack request", "error", err)
		return
	}

	nackReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queueURL+"/jobs/"+jobID+"/fail", bytes.NewReader(body))
	if err != nil {
		log.Error("failed to build nack request", "error", err)
		return
	}
	nackReq.Header.Set("Content-Type", "application/json")

	nackResp, err := httpClient.Do(nackReq)
	if err != nil {
		log.Error("failed to nack job", "error", err)
		return
	}
	defer func() { _ = nackResp.Body.Close() }()

	var result struct {
		PermanentlyFailed bool `json:"permanently_failed"`
	}
	if err := json.NewDecoder(nackResp.Body).Decode(&result); err != nil {
		log.Error("failed to decode nack response", "error", err)
		return
	}

	if result.PermanentlyFailed {
		log.Error("job permanently failed, notifying engine")
		notifyEngineOfFailure(ctx, httpClient, engineURL, payload, reason, log)
	}
}

// notifyEngineOfFailure tells the engine a step is never coming back,
// via the failure counterpart to the success webhook pollOnce already
// sends on a normal completion.
func notifyEngineOfFailure(ctx context.Context, httpClient *http.Client, engineURL string, payload worker.StepPayload, reason string, log *slog.Logger) {
	webhookPayload := worker.WebhookFailedPayload{
		RunID:  payload.RunID,
		StepID: payload.StepID,
		Reason: reason,
	}
	webhookJSON, err := json.Marshal(webhookPayload)
	if err != nil {
		log.Error("failed to marshal failure webhook payload", "error", err)
		return
	}

	webhookReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL+"/webhook/failed", bytes.NewReader(webhookJSON))
	if err != nil {
		log.Error("failed to build failure webhook request", "error", err)
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookResp, err := httpClient.Do(webhookReq)
	if err != nil {
		log.Error("failed to notify engine of failure", "error", err)
		return
	}
	_ = webhookResp.Body.Close()
}

// StepRunner executes a step's payload and returns its output. Each job
// type registered in main's runners map has one of these.
type StepRunner func(ctx context.Context, payload worker.StepPayload) (string, error)

// newToolCallRunner returns a StepRunner that dispatches a tool_call
// step to the tool named by payload.Tool in registry, so a new tool
// can be added by registering it here rather than modifying dispatch
// logic itself (#15).
func newToolCallRunner(registry map[string]tools.Tool) StepRunner {
	return func(ctx context.Context, payload worker.StepPayload) (string, error) {
		tool, ok := registry[payload.Tool]
		if !ok {
			return "", fmt.Errorf("tool_call: unknown or unavailable tool %q", payload.Tool)
		}
		output, err := tool.Run(ctx, payload.Input)
		if err != nil {
			return "", fmt.Errorf("tool_call (%s): %w", payload.Tool, err)
		}
		return output, nil
	}
}

// newLLMRunner returns a StepRunner that sends payload.Input (the resolved
// prompt template) to client and returns its completion as the step output.
func newLLMRunner(client llm.Client) StepRunner {
	return func(ctx context.Context, payload worker.StepPayload) (string, error) {
		output, err := client.Complete(ctx, payload.Input)
		if err != nil {
			return "", fmt.Errorf("llm_call: %w", err)
		}
		return output, nil
	}
}

// newSupervisorRunner returns a StepRunner that asks client to choose
// exactly one of payload.Options or SupervisorDoneSignal, using
// Decide's structured-output constraint rather than a plain Complete
// call the engine would then have to exact-match unaided.
func newSupervisorRunner(client llm.Client) StepRunner {
	return func(ctx context.Context, payload worker.StepPayload) (string, error) {
		choices := append(append([]string{}, payload.Options...), worker.SupervisorDoneSignal)
		decision, err := client.Decide(ctx, payload.Input, choices)
		if err != nil {
			return "", fmt.Errorf("supervisor: %w", err)
		}
		return decision, nil
	}
}
