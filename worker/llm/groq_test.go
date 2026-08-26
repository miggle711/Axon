package llm

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestGroqClient(t *testing.T, handler http.HandlerFunc) (*GroqClient, func()) {
	t.Helper()
	server := httptest.NewServer(handler)
	client := &GroqClient{apiKey: "test-key", model: "test-model", httpClient: server.Client()}
	// Redirect requests to the test server instead of the real Groq endpoint.
	client.baseURL = server.URL
	return client, server.Close
}

func TestGroqComplete_Success(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hi there"}}]}`))
	})
	defer closeServer()

	output, err := client.Complete(context.Background(), "hello")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "hi there" {
		t.Errorf("got %q, want %q", output, "hi there")
	}
}

func TestGroqComplete_RateLimited(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	})
	defer closeServer()

	_, err := client.Complete(context.Background(), "hello")
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v, want ErrRateLimited", err)
	}
}

func TestGroqComplete_APIError(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid model","type":"invalid_request_error"}}`))
	})
	defer closeServer()

	_, err := client.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestGroqComplete_MalformedResponse(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`not json`))
	})
	defer closeServer()

	_, err := client.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestGroqComplete_NoChoices(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[]}`))
	})
	defer closeServer()

	_, err := client.Complete(context.Background(), "hello")
	if err == nil {
		t.Fatal("expected an error, got none")
	}
}

func TestGroqDecide_Success(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if !strings.Contains(string(body), `"response_format":{"type":"json_object"}`) {
			t.Errorf("expected request to set JSON mode response_format, got body: %s", body)
		}
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"decision\": \"search\"}"}}]}`))
	})
	defer closeServer()

	decision, err := client.Decide(context.Background(), "should we search again?", []string{"search", "done"})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if decision != "search" {
		t.Errorf("got %q, want %q", decision, "search")
	}
}

func TestGroqDecide_ValueNotInOptions(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"{\"decision\": \"maybe\"}"}}]}`))
	})
	defer closeServer()

	_, err := client.Decide(context.Background(), "should we search again?", []string{"search", "done"})
	if err == nil {
		t.Fatal("expected an error when the decision isn't one of options, got none")
	}
}

func TestGroqDecide_NotJSON(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"search"}}]}`))
	})
	defer closeServer()

	_, err := client.Decide(context.Background(), "should we search again?", []string{"search", "done"})
	if err == nil {
		t.Fatal("expected an error when the model's content isn't JSON, got none")
	}
}

func TestGroqDecide_RateLimited(t *testing.T) {
	client, closeServer := newTestGroqClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = w.Write([]byte(`{"error":{"message":"rate limited","type":"rate_limit_error"}}`))
	})
	defer closeServer()

	_, err := client.Decide(context.Background(), "should we search again?", []string{"search", "done"})
	if !errors.Is(err, ErrRateLimited) {
		t.Errorf("got %v, want ErrRateLimited", err)
	}
}
