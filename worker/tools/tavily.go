package tools

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

const tavilyBaseURL = "https://api.tavily.com/search"

// TavilySearch calls the Tavily web search API and formats the results
// as text, since Tool.Run's contract is text-in/text-out like every
// other step type in this system.
type TavilySearch struct {
	apiKey     string
	httpClient *http.Client
	baseURL    string // overridable in tests; defaults to tavilyBaseURL
}

func NewTavilySearch(apiKey string, httpClient *http.Client) *TavilySearch {
	return &TavilySearch{apiKey: apiKey, httpClient: httpClient, baseURL: tavilyBaseURL}
}

type tavilyRequest struct {
	APIKey     string `json:"api_key"`
	Query      string `json:"query"`
	MaxResults int    `json:"max_results,omitempty"`
}

type tavilyResult struct {
	Title   string `json:"title"`
	URL     string `json:"url"`
	Content string `json:"content"`
}

type tavilyResponse struct {
	Answer  string         `json:"answer"`
	Results []tavilyResult `json:"results"`
	Error   string         `json:"error"`
	Detail  *struct {
		Error string `json:"error"`
	} `json:"detail"`
}

// ErrRateLimited indicates Tavily rejected the request due to rate limiting (HTTP 429).
var ErrRateLimited = errors.New("tavily: rate limited")

func (t *TavilySearch) Run(ctx context.Context, input string) (string, error) {
	reqBody, err := json.Marshal(tavilyRequest{APIKey: t.apiKey, Query: input, MaxResults: 5})
	if err != nil {
		return "", fmt.Errorf("tavily: failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, t.baseURL, bytes.NewReader(reqBody))
	if err != nil {
		return "", fmt.Errorf("tavily: failed to build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := t.httpClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("tavily: request failed: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("tavily: failed to read response body: %w", err)
	}

	if resp.StatusCode == http.StatusTooManyRequests {
		return "", ErrRateLimited
	}

	var parsed tavilyResponse
	if err := json.Unmarshal(respBody, &parsed); err != nil {
		return "", fmt.Errorf("tavily: malformed response (status %d): %w", resp.StatusCode, err)
	}

	if parsed.Error != "" {
		return "", fmt.Errorf("tavily: API error: %s", parsed.Error)
	}
	if parsed.Detail != nil && parsed.Detail.Error != "" {
		return "", fmt.Errorf("tavily: API error: %s", parsed.Detail.Error)
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("tavily: unexpected status %d", resp.StatusCode)
	}

	return formatResults(parsed), nil
}

func formatResults(resp tavilyResponse) string {
	var b strings.Builder
	if resp.Answer != "" {
		b.WriteString(resp.Answer)
		b.WriteString("\n\n")
	}
	for i, r := range resp.Results {
		fmt.Fprintf(&b, "%d. %s (%s)\n%s\n", i+1, r.Title, r.URL, r.Content)
		if i < len(resp.Results)-1 {
			b.WriteString("\n")
		}
	}
	return strings.TrimSpace(b.String())
}
