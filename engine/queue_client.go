package engine
import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"
)

type enqueueRequest struct {
	// EnqueueRequest is what the Orchestrator sends to POST /jobs in the Queue service
    Type     string `json:"type"`
    Payload  string `json:"payload"`
    Priority int    `json:"priority"`
}

type jobResponse struct {
    ID string `json:"id"`
}

type QueueClient struct {
    baseURL    string
    httpClient *http.Client
}

func NewQueueClient(baseURL string) *QueueClient {
    return &QueueClient{
        baseURL:    baseURL,
        httpClient: &http.Client{Timeout: 10 * time.Second},
    }
}

func (c *QueueClient) Enqueue(ctx context.Context, stepType string, payload StepPayload, priority int) (string, error) {
	/**
	Enqueue sends a request to the Queue service to enqueue a job of the specified type with the given payload and priority.
	It returns the job ID of the enqueued job or an error if the operation fails.

	arguments:
	- ctx: context for request cancellation and timeouts
	- stepType: the type of the step (e.g., "tool_call", "llm_call", etc.)
	- payload: the StepPayload containing the data for the step
	- priority: the priority of the job (higher means more important)

	returns:
	- job ID of the enqueued job
	- error if any occurred during the enqueue operation
	**/
	// Convert the StepPayload to JSON
    payloadJSON, err := json.Marshal(payload)
    if err != nil {
        return "", err
    }

	// Create the request body for the enqueue request
    reqBody, err := json.Marshal(enqueueRequest{
        Type:     stepType,
        Payload:  string(payloadJSON),
        Priority: priority,
    })
    if err != nil {
        return "", err
    }

	// Create the queue request to the queue service
    req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/jobs", bytes.NewReader(reqBody))
    if err != nil {
        return "", err
    }
    req.Header.Set("Content-Type", "application/json")

	// Send the request and handle the response
    resp, err := c.httpClient.Do(req)
    if err != nil {
        return "", err
    }
    defer resp.Body.Close() // Ensure the response body is closed to prevent resource leaks

	// Check if the response status code indicates success
    if resp.StatusCode != http.StatusCreated {
        return "", fmt.Errorf("enqueue failed: status %d", resp.StatusCode)
    }

	// Decode the response to get the job ID
    var jobResp jobResponse
    if err := json.NewDecoder(resp.Body).Decode(&jobResp); err != nil {
        return "", err
    }
	// Return the job ID from the response for tracking
    return jobResp.ID, nil 
}

