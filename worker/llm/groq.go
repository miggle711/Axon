package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
)

const groqBaseURL = "https://api.groq.com/openai/v1/chat/completions"

// GroqClient calls Groq's OpenAI-compatible chat completions API.
type GroqClient struct {
	apiKey     string
	model      string
	httpClient *http.Client
	baseURL    string // overridable in tests; defaults to groqBaseURL
}

func NewGroqClient(apiKey, model string, httpClient *http.Client) *GroqClient {
	return &GroqClient{apiKey: apiKey, model: model, httpClient: httpClient, baseURL: groqBaseURL}
}

type groqMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type groqRequest struct {
	Model    string        `json:"model"`
	Messages []groqMessage `json:"messages"`
}

type groqResponse struct {
	Choices []struct {
		Message groqMessage `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// ErrRateLimited indicates the Groq API rejected the request due to rate limiting (HTTP 429).
var ErrRateLimited = errors.New("groq: rate limited")

func (c *GroqClient) Complete(ctx context.Context, prompt string) (string, error) {
	reqBody, err := json.Marshal(groqRequest{
		Model:    c.model,
		Messages: []groqMessage{{Role: "user", Content: prompt}},
	})
	if err != nil {
		return "", fmt.Errorf("groq: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("groq: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("groq: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("groq: failed to read response body: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}

	var parsed groqResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("groq: malformed response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != nil {
		return "", fmt.Errorf("groq: API error (%s): %s", parsed.Error.Type, parsed.Error.Message)
	}

	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("groq: unexpected status %d", resp.StatusCode)
	}

	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("groq: response contained no choices")
	}

	return parsed.Choices[0].Message.Content, nil
}
