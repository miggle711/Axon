// Package cli implements a thin HTTP client for the engine's API, used
// by cmd/main.go's command dispatch.
package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	engine "axon-engine"
	"axon-engine/api"
)

// Client calls the engine's HTTP API.
type Client struct {
	baseURL    string
	httpClient *http.Client
}

func NewClient(baseURL string) *Client {
	return &Client{baseURL: baseURL, httpClient: &http.Client{Timeout: 10 * time.Second}}
}

// CreateRunByName starts a run for the named agent, resolved
// server-side via the engine's AgentRegistry (see #40).
func (c *Client) CreateRunByName(ctx context.Context, agentName, input string) (*engine.Run, error) {
	reqBody, err := json.Marshal(api.CreateRunRequest{AgentName: agentName, Input: input})
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}
	return c.postRun(ctx, reqBody)
}

func (c *Client) postRun(ctx context.Context, reqBody []byte) (*engine.Run, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/runs", bytes.NewReader(reqBody))
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusCreated {
		return nil, apiError(resp.StatusCode, body)
	}

	var run engine.Run
	if err := json.Unmarshal(body, &run); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &run, nil
}

// GetRun fetches a run's current state.
func (c *Client) GetRun(ctx context.Context, runID string) (*engine.Run, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/runs/"+runID, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to build request: %w", err)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, apiError(resp.StatusCode, body)
	}

	var run engine.Run
	if err := json.Unmarshal(body, &run); err != nil {
		return nil, fmt.Errorf("failed to parse response: %w", err)
	}
	return &run, nil
}

func apiError(statusCode int, body []byte) error {
	var errResp api.ErrorResponse
	if err := json.Unmarshal(body, &errResp); err == nil && errResp.Error != "" {
		return fmt.Errorf("engine returned %d: %s", statusCode, errResp.Error)
	}
	return fmt.Errorf("engine returned %d: %s", statusCode, string(body))
}
