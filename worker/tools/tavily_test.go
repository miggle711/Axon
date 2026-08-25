package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestTavilySearch(t *testing.T, handler http.HandlerFunc) (*TavilySearch, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	tool := &TavilySearch{apiKey: "test-key", httpClient: server.Client()}
	// Redirect requests to the test server instead of the real Tavily endpoint.
	tool.baseURL = server.URL
	return tool, server.Close
}

func TestTavilySearch_Success(t *testing.T) {
	tool, closeServer := newTestTavilySearch(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{
			"answer": "Go is a statically typed language.",
			"results": [
				{"title": "The Go Programming Language", "url": "https://go.dev", "content": "Go is an open source language."}
			]
		}`))
	})
	defer closeServer()

	output, err := tool.Run(context.Background(), "what is go")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output == "" {
		t.Fatal("expected non-empty formatted output")
	}
	if !strings.Contains(output, "Go is a statically typed language.") {
		t.Errorf("expected output to include the answer, got: %q", output)
	}
	if !strings.Contains(output, "The Go Programming Language") {
		t.Errorf("expected output to include the result title, got: %q", output)
	}
}

func TestTavilySearch_RateLimited(t *testing.T) {
	tool, closeServer := newTestTavilySearch(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"detail": {"error": "rate limited"}}`))
	})
	defer closeServer()

	_, err := tool.Run(context.Background(), "query")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v, want ErrRateLimited", err)
	}
}

func TestTavilySearch_APIError(t *testing.T) {
	tool, closeServer := newTestTavilySearch(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"detail": {"error": "invalid api key"}}`))
	})
	defer closeServer()

	_, err := tool.Run(context.Background(), "query")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestTavilySearch_MalformedResponse(t *testing.T) {
	tool, closeServer := newTestTavilySearch(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})
	defer closeServer()

	_, err := tool.Run(context.Background(), "query")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}
