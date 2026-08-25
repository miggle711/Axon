package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"time"

	worker "axon-worker"
	"axon-worker/llm"
)

func main() {
	ctx := context.Background()

	// Parse command-line flags for the queue and engine service URLs
	queueURL := flag.String("queue", "http://localhost:8080", "Queue service URL")
	engineURL := flag.String("engine", "http://localhost:8000", "Engine service URL")
	groqModel := flag.String("model", "openai/gpt-oss-120b", "Groq model to use for llm_call steps")
	flag.Parse()

	httpClient := &http.Client{Timeout: 10 * time.Second}

	runners := map[string]StepRunner{
		worker.JobTypeToolCall: runToolCall,
	}

	if apiKey := os.Getenv("GROQ_API_KEY"); apiKey != "" {
		groqClient := llm.NewGroqClient(apiKey, *groqModel, httpClient)
		llmRunner := newLLMRunner(groqClient)
		runners[worker.JobTypeLLMCall] = llmRunner
		// supervisor steps are decided by the same prompt-in/text-out
		// call as llm_call; the engine is what interprets the output
		// as a routing decision, not the worker.
		runners[worker.JobTypeSupervisor] = llmRunner
	} else {
		log.Printf("GROQ_API_KEY not set: llm_call and supervisor jobs will be nacked until it is provided")
	}

	for {
		pollOnce(ctx, httpClient, *queueURL, *engineURL, runners)
	}

}

func pollOnce(ctx context.Context, httpClient *http.Client, queueURL, engineURL string, runners map[string]StepRunner) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, queueURL+"/jobs/next", nil)
	if err != nil {
		log.Printf("failed to build request: %v", err)
		return
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		log.Printf("failed to poll queue: %v", err)
		time.Sleep(time.Second)
		return
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusNoContent {
		time.Sleep(time.Second) // nothing pending, wait before polling again
		return
	}

	if resp.StatusCode != http.StatusOK {
		log.Printf("unexpected status from queue: %d", resp.StatusCode)
		time.Sleep(time.Second)
		return
	}

	var job worker.JobResponse
	if err := json.NewDecoder(resp.Body).Decode(&job); err != nil {
		log.Printf("failed to decode job: %v", err)
		return
	}

	var payload worker.StepPayload
	if err := json.Unmarshal([]byte(job.Payload), &payload); err != nil {
		log.Printf("failed to unmarshal job payload: %v", err)
		return
	}

	runner, ok := runners[job.Type]
	if !ok {
		reason := fmt.Sprintf("unsupported job type: %s", job.Type)
		log.Printf("failed to run step %s: %s", payload.StepID, reason)
		nackJob(ctx, httpClient, queueURL, job.ID, reason)
		return
	}

	output, err := runner(ctx, payload)
	if err != nil {
		log.Printf("failed to run step %s: %v", payload.StepID, err)
		nackJob(ctx, httpClient, queueURL, job.ID, err.Error())
		return
	}

	// Ack the job with the queue
	ackReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queueURL+"/jobs/"+job.ID+"/ack", nil)
	if err != nil {
		log.Printf("failed to build ack request: %v", err)
		return
	}
	ackResp, err := httpClient.Do(ackReq)
	if err != nil {
		log.Printf("failed to ack job: %v", err)
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
		log.Printf("failed to marshal webhook payload: %v", err)
		return
	}

	webhookReq, err := http.NewRequestWithContext(ctx, http.MethodPost, engineURL+"/webhook/complete", bytes.NewReader(webhookJSON))
	if err != nil {
		log.Printf("failed to build webhook request: %v", err)
		return
	}
	webhookReq.Header.Set("Content-Type", "application/json")
	webhookResp, err := httpClient.Do(webhookReq)
	if err != nil {
		log.Printf("failed to notify engine: %v", err)
		return
	}
	_ = webhookResp.Body.Close()

	log.Printf("processed step %s for run %s", payload.StepID, payload.RunID)
}

// nackJob marks a job as failed with the queue so it can be retried or
// surfaced as an error instead of sitting stuck in the running set.
func nackJob(ctx context.Context, httpClient *http.Client, queueURL, jobID, reason string) {
	body, err := json.Marshal(map[string]string{"reason": reason})
	if err != nil {
		log.Printf("failed to marshal nack request: %v", err)
		return
	}

	nackReq, err := http.NewRequestWithContext(ctx, http.MethodPost, queueURL+"/jobs/"+jobID+"/fail", bytes.NewReader(body))
	if err != nil {
		log.Printf("failed to build nack request: %v", err)
		return
	}
	nackReq.Header.Set("Content-Type", "application/json")

	nackResp, err := httpClient.Do(nackReq)
	if err != nil {
		log.Printf("failed to nack job: %v", err)
		return
	}
	_ = nackResp.Body.Close()
}

// StepRunner executes a step's payload and returns its output. Each job
// type registered in main's runners map has one of these.
type StepRunner func(ctx context.Context, payload worker.StepPayload) (string, error)

// runToolCall is the tool_call stub: it echoes the input back as output.
func runToolCall(ctx context.Context, payload worker.StepPayload) (string, error) {
	return payload.Input, nil
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
