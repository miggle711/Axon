package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	worker "axon-worker"
	"axon-worker/tools"
)

func TestToolCallRunner(t *testing.T) {
	ctx := context.Background()
	registry := map[string]tools.Tool{"echo": tools.Echo{}}
	runner := newToolCallRunner(registry)

	t.Run("dispatches to the named tool", func(t *testing.T) {
		output, err := runner(ctx, worker.StepPayload{Tool: "echo", Input: "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "hello" {
			t.Errorf("got %q, want %q", output, "hello")
		}
	})

	t.Run("unknown tool errors instead of falling back", func(t *testing.T) {
		if _, err := runner(ctx, worker.StepPayload{Tool: "does_not_exist", Input: "hello"}); err == nil {
			t.Error("expected an error for an unregistered tool, got none")
		}
	})
}

type fakeLLMClient struct {
	output    string
	err       error
	decision  string
	decideErr error
}

func (f *fakeLLMClient) Complete(ctx context.Context, prompt string) (string, error) {
	return f.output, f.err
}

func (f *fakeLLMClient) Decide(ctx context.Context, prompt string, options []string) (string, error) {
	return f.decision, f.decideErr
}

func TestLLMRunner(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "prompt"}

	runner := newLLMRunner(&fakeLLMClient{output: "a real completion"})
	output, err := runner(ctx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "a real completion" {
		t.Errorf("got %q, want %q", output, "a real completion")
	}

	failingRunner := newLLMRunner(&fakeLLMClient{err: errors.New("boom")})
	if _, err := failingRunner(ctx, payload); err == nil {
		t.Error("expected an error when the client fails, got none")
	}
}

func TestSupervisorRunner(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "prompt", Options: []string{"search"}}

	runner := newSupervisorRunner(&fakeLLMClient{decision: "search"})
	output, err := runner(ctx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "search" {
		t.Errorf("got %q, want %q", output, "search")
	}

	failingRunner := newSupervisorRunner(&fakeLLMClient{decideErr: errors.New("boom")})
	if _, err := failingRunner(ctx, payload); err == nil {
		t.Error("expected an error when the client fails, got none")
	}
}

// newFakeQueueServer stands in for the queue's GET /jobs/next and
// POST /jobs/{id}/ack endpoints, always handing back one echo tool_call
// job then acking cleanly - enough to drive pollOnce all the way to its
// completion webhook.
func newFakeQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	served := false
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/jobs/next":
			if served {
				w.WriteHeader(http.StatusNoContent)
				return
			}
			served = true
			payload, _ := json.Marshal(worker.StepPayload{RunID: "run-1", StepID: "step-1", Tool: "echo", Input: "hello"})
			_ = json.NewEncoder(w).Encode(worker.JobResponse{ID: "job-1", Type: worker.JobTypeToolCall, Payload: string(payload)})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/ack"):
			w.WriteHeader(http.StatusOK)
		default:
			t.Fatalf("unexpected queue request: %s %s", r.Method, r.URL.Path)
		}
	}))
}

// TestPollOnce_WebhookFailureIsLogged covers #58: pollOnce used to
// discard the completion webhook's response entirely once the POST
// itself succeeded at the network level, so an engine-side rejection
// (a non-200) was silently treated as a normal success - the run would
// then sit stuck with no error anywhere on the worker side. Confirms
// pollOnce now logs the failure instead of logging "processed step".
func TestPollOnce_WebhookFailureIsLogged(t *testing.T) {
	queueServer := newFakeQueueServer(t)
	defer queueServer.Close()

	engineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error":"failed to mark step as complete"}`))
	}))
	defer engineServer.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	runners := map[string]StepRunner{
		worker.JobTypeToolCall: newToolCallRunner(map[string]tools.Tool{"echo": tools.Echo{}}),
	}

	pollOnce(context.Background(), &http.Client{Timeout: 5 * time.Second}, queueServer.URL, engineServer.URL, runners, logger)

	logOutput := logBuf.String()
	if strings.Contains(logOutput, `"msg":"processed step"`) {
		t.Errorf("expected pollOnce NOT to log success when the engine rejected the webhook, got log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, `"msg":"engine rejected completion webhook"`) {
		t.Errorf("expected pollOnce to log the engine's rejection, got log output: %s", logOutput)
	}
	if !strings.Contains(logOutput, `"status":500`) {
		t.Errorf("expected the logged status to be 500, got log output: %s", logOutput)
	}
}

// TestPollOnce_WebhookSuccessIsLogged is the mirror case: a real 200
// from the engine should still log "processed step" as before.
func TestPollOnce_WebhookSuccessIsLogged(t *testing.T) {
	queueServer := newFakeQueueServer(t)
	defer queueServer.Close()

	engineServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer engineServer.Close()

	var logBuf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&logBuf, nil))
	runners := map[string]StepRunner{
		worker.JobTypeToolCall: newToolCallRunner(map[string]tools.Tool{"echo": tools.Echo{}}),
	}

	pollOnce(context.Background(), &http.Client{Timeout: 5 * time.Second}, queueServer.URL, engineServer.URL, runners, logger)

	if !strings.Contains(logBuf.String(), `"msg":"processed step"`) {
		t.Errorf("expected pollOnce to log success on a real 200, got log output: %s", logBuf.String())
	}
}
