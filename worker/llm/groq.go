package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
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

type groqResponseFormat struct {
	Type string `json:"type"`
}

type groqRequest struct {
	Model          string              `json:"model"`
	Messages       []groqMessage       `json:"messages"`
	ResponseFormat *groqResponseFormat `json:"response_format,omitempty"`
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

// chatCompletion sends req and returns the first choice's message
// content, handling the response parsing/error cases shared by
// Complete and Decide.
func (c *GroqClient) chatCompletion(ctx context.Context, req groqRequest) (string, error) {
	req.Model = c.model

	reqBody, err := json.Marshal(req)
	if err != nil {
		return "", fmt.Errorf("groq: failed to marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("groq: failed to build request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(httpReq)
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

func (c *GroqClient) Complete(ctx context.Context, prompt string) (string, error) {
	return c.chatCompletion(ctx, groqRequest{
		Messages: []groqMessage{{Role: "user", Content: prompt}},
	})
}

type decideResponse struct {
	Decision string `json:"decision"`
}

// Decide asks the model to pick exactly one of options, using Groq's
// JSON mode (response_format: json_object) to constrain the reply to
// valid JSON at the API level, rather than relying solely on the
// prompt asking nicely for one bare word (see #39's finding that plain
// prompt-only constraint works but has no structural guarantee).
//
// JSON mode only guarantees syntactically valid JSON, not this
// specific {"decision": "..."} shape or that the value is one of
// options - the prompt still has to ask for that shape, and the value
// is validated against options after parsing.
func (c *GroqClient) Decide(ctx context.Context, prompt string, options []string) (string, error) {
	fullPrompt := fmt.Sprintf(
		"%s\n\nRespond with a JSON object of the exact form {\"decision\": \"<value>\"}, where <value> is exactly one of: %s. Respond with nothing else.",
		prompt, strings.Join(options, ", "),
	)

	raw, err := c.chatCompletion(ctx, groqRequest{
		Messages:       []groqMessage{{Role: "user", Content: fullPrompt}},
		ResponseFormat: &groqResponseFormat{Type: "json_object"},
	})
	if err != nil {
		return "", err
	}

	var parsed decideResponse
	if err := json.Unmarshal([]byte(raw), &parsed); err != nil {
		return "", fmt.Errorf("groq: decide response was not valid JSON: %w", err)
	}

	for _, opt := range options {
		if parsed.Decision == opt {
			return parsed.Decision, nil
		}
	}
	return "", fmt.Errorf("groq: decide response %q did not match any of %v", parsed.Decision, options)
}
