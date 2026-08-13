package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net/http"
	"time"

	worker "axon-worker"
)

func main() {
	ctx := context.Background()

	// Parse command-line flags for the queue and engine service URLs
	queueURL := flag.String("queue", "http://localhost:8080", "Queue service URL")
	engineURL := flag.String("engine", "http://localhost:8000", "Engine service URL")
	flag.Parse()

	httpClient := &http.Client{Timeout: 10 * time.Second}

	for {
		pollOnce(ctx, httpClient, *queueURL, *engineURL)
	}

}

func pollOnce(ctx context.Context, httpClient *http.Client, queueURL, engineURL string) {
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

	output, err := runStep(ctx, job.Type, payload)
	if err != nil {
		log.Printf("failed to run step %s: %v", payload.StepID, err)
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

// runStep executes payload according to jobType, returning the step's
// output. tool_call runs the echo stub; llm_call is a placeholder
// until a real LLM client is wired in (see #26).
func runStep(ctx context.Context, jobType string, payload worker.StepPayload) (string, error) {
	switch jobType {
	case worker.JobTypeLLMCall:
		return "[llm_call stub] " + payload.Input, nil
	case worker.JobTypeToolCall:
		return payload.Input, nil
	default:
		return "", fmt.Errorf("unsupported job type: %s", jobType)
	}
}
