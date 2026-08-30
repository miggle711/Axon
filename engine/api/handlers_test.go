package api

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	engine "axon-engine"
)

// discardLogger silences log output for tests that don't assert on it.
var discardLogger = slog.New(slog.DiscardHandler)

type fakeRunStore struct {
	runs map[string]*engine.Run
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{runs: make(map[string]*engine.Run)}
}

func (s *fakeRunStore) SaveRun(ctx context.Context, run *engine.Run) error {
	s.runs[run.ID] = run
	return nil
}

func (s *fakeRunStore) GetRun(ctx context.Context, runID string) (*engine.Run, error) {
	return s.runs[runID], nil
}

func newFakeQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	counter := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "job-" + string(rune('0'+counter))})
	}))
}

func newTestServer(t *testing.T, agents engine.AgentRegistry) *Server {
	t.Helper()
	queueServer := newFakeQueueServer(t)
	t.Cleanup(queueServer.Close)

	orchestrator := engine.NewOrchestrator(newFakeRunStore(), engine.NewQueueClient(queueServer.URL), agents, discardLogger)
	return NewServer(orchestrator)
}

func doRequest(t *testing.T, server *Server, method, path string, body []byte) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	server.router.ServeHTTP(w, req)
	return w
}

func TestCreateRunHandler_ByAgentName(t *testing.T) {
	def := engine.AgentDefinition{
		Name:  "greeter",
		Steps: []engine.StepDefinition{{ID: "greet", Type: engine.StepTypeToolCall, Tool: "echo", InputTemplate: "hi", DependsOn: []string{}}},
	}
	server := newTestServer(t, engine.MapAgentRegistry{"greeter": def})

	body, _ := json.Marshal(map[string]string{"agent_name": "greeter", "input": "world"})
	w := doRequest(t, server, http.MethodPost, "/runs", body)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var run engine.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if run.AgentName != "greeter" {
		t.Errorf("expected AgentName 'greeter', got %q", run.AgentName)
	}
}

func TestCreateRunHandler_ByInlineDefinition(t *testing.T) {
	server := newTestServer(t, engine.MapAgentRegistry{})

	body, _ := json.Marshal(map[string]interface{}{
		"definition": engine.AgentDefinition{
			Name:  "inline_agent",
			Steps: []engine.StepDefinition{{ID: "s1", Type: engine.StepTypeToolCall, Tool: "echo", InputTemplate: "x", DependsOn: []string{}}},
		},
		"input": "hello",
	})
	w := doRequest(t, server, http.MethodPost, "/runs", body)

	if w.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", w.Code, w.Body.String())
	}
	var run engine.Run
	if err := json.Unmarshal(w.Body.Bytes(), &run); err != nil {
		t.Fatalf("failed to decode response: %v", err)
	}
	if run.AgentName != "inline_agent" {
		t.Errorf("expected AgentName 'inline_agent', got %q", run.AgentName)
	}
}

func TestCreateRunHandler_RejectsNeitherFieldSet(t *testing.T) {
	server := newTestServer(t, engine.MapAgentRegistry{})

	body, _ := json.Marshal(map[string]string{"input": "hello"})
	w := doRequest(t, server, http.MethodPost, "/runs", body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when neither agent_name nor definition is set, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateRunHandler_RejectsBothFieldsSet(t *testing.T) {
	server := newTestServer(t, engine.MapAgentRegistry{})

	body, _ := json.Marshal(map[string]interface{}{
		"agent_name": "greeter",
		"definition": engine.AgentDefinition{Name: "x", Steps: []engine.StepDefinition{}},
		"input":      "hello",
	})
	w := doRequest(t, server, http.MethodPost, "/runs", body)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when both agent_name and definition are set, got %d: %s", w.Code, w.Body.String())
	}
}

func TestCreateRunHandler_UnknownAgentName(t *testing.T) {
	server := newTestServer(t, engine.MapAgentRegistry{})

	body, _ := json.Marshal(map[string]string{"agent_name": "does_not_exist", "input": "hello"})
	w := doRequest(t, server, http.MethodPost, "/runs", body)

	if w.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 for an unknown agent name, got %d: %s", w.Code, w.Body.String())
	}
}
