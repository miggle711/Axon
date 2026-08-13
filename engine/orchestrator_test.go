package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// fakeRunStore is an in-memory RunStore for testing, avoiding a real
// Redis dependency.
type fakeRunStore struct {
	mu   sync.Mutex
	runs map[string]*Run
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{runs: make(map[string]*Run)}
}

func (s *fakeRunStore) SaveRun(ctx context.Context, run *Run) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = run
	return nil
}

func (s *fakeRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.runs[runID], nil
}

// newFakeQueueServer stands in for the queue service's POST /jobs
// endpoint, always returning a fresh job ID with 201.
func newFakeQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	counter := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter++
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(map[string]string{"id": "job-" + string(rune('0'+counter))})
	}))
}

func conditionalTestAgentSteps() []StepDefinition {
	return []StepDefinition{
		{
			ID:            "step_1",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "success",
			DependsOn:     []string{},
		},
		{
			ID:        "check",
			Type:      StepTypeConditional,
			DependsOn: []string{"step_1"},
			Condition: "{{step_1.output}} == success",
			OnTrue:    "on_true_step",
			OnFalse:   "on_false_step",
		},
		{
			ID:            "on_true_step",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "took the true branch",
			DependsOn:     []string{"check"},
		},
		{
			ID:            "on_false_step",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "took the false branch",
			DependsOn:     []string{"check"},
		},
	}
}

// TestOnStepCompleted_ConditionalRouting exercises the same 4-step
// agent used in live e2e testing (step_1 -> check (conditional) ->
// on_true_step / on_false_step), directly through OnStepCompleted,
// bypassing HTTP/Redis so it isn't affected by the concurrency race
// tracked in issue #19.
func TestOnStepCompleted_ConditionalRouting(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL))

	agent := AgentDefinition{
		Name:  "conditional_test_agent",
		Steps: conditionalTestAgentSteps(),
	}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if _, enqueued := run.EnqueuedSteps["step_1"]; !enqueued {
		t.Fatalf("expected step_1 to be enqueued after CreateRun, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
	if len(run.CompletedSteps) != 0 {
		t.Fatalf("expected no completed steps yet, got %v", run.CompletedSteps)
	}

	// Simulate the worker completing step_1.
	err = orchestrator.OnStepCompleted(ctx, WebhookPayload{
		RunID:  run.ID,
		StepID: "step_1",
		Output: "success",
	})
	if err != nil {
		t.Fatalf("OnStepCompleted(step_1) failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	// step_1 should be completed, check should have resolved inline
	// (true branch, since output == "success"), on_true_step should
	// be enqueued, and on_false_step (plus nothing transitively
	// beyond it) should be skipped.
	if run.StepResults["step_1"] != "success" {
		t.Errorf("expected step_1 result 'success', got %q", run.StepResults["step_1"])
	}
	if run.StepResults["check"] != "on_true_step" {
		t.Errorf("expected check result 'on_true_step', got %q", run.StepResults["check"])
	}
	if _, enqueued := run.EnqueuedSteps["on_true_step"]; !enqueued {
		t.Errorf("expected on_true_step to be enqueued, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
	if _, enqueued := run.EnqueuedSteps["on_false_step"]; enqueued {
		t.Errorf("expected on_false_step NOT to be enqueued, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	skipped := map[string]bool{}
	for _, id := range run.SkippedSteps {
		skipped[id] = true
	}
	if !skipped["on_false_step"] {
		t.Errorf("expected on_false_step in SkippedSteps, got %v", run.SkippedSteps)
	}

	// Simulate the worker completing on_true_step.
	err = orchestrator.OnStepCompleted(ctx, WebhookPayload{
		RunID:  run.ID,
		StepID: "on_true_step",
		Output: "took the true branch",
	})
	if err != nil {
		t.Fatalf("OnStepCompleted(on_true_step) failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	if run.Status != "completed" {
		t.Errorf("expected run status 'completed', got %q (completed=%v skipped=%v steps=%d)",
			run.Status, run.CompletedSteps, run.SkippedSteps, len(run.Steps))
	}
}

func TestEvaluateCondition(t *testing.T) {
	run := &Run{
		UserInput:   "hello",
		StepResults: map[string]string{"step_1": "success"},
	}

	cases := []struct {
		name      string
		condition string
		want      bool
		wantErr   bool
	}{
		{"equal true", "{{step_1.output}} == success", true, false},
		{"equal false", "{{step_1.output}} == failure", false, false},
		{"not equal true", "{{step_1.output}} != failure", true, false},
		{"not equal false", "{{step_1.output}} != success", false, false},
		{"user input", "{{user_input}} == hello", true, false},
		{"malformed", "{{step_1.output}} success", false, true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := evaluateCondition(tc.condition, run)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got none")
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got != tc.want {
				t.Errorf("evaluateCondition(%q) = %v, want %v", tc.condition, got, tc.want)
			}
		})
	}
}

func TestFindTransitiveDependents(t *testing.T) {
	steps := []StepDefinition{
		{ID: "a", DependsOn: []string{}},
		{ID: "b", DependsOn: []string{"a"}},
		{ID: "c", DependsOn: []string{"b"}},
		{ID: "d", DependsOn: []string{"a"}},
		{ID: "e", DependsOn: []string{}}, // unrelated
	}

	got := findTransitiveDependents("a", steps)

	want := map[string]bool{"b": true, "c": true, "d": true}
	if len(got) != len(want) {
		t.Fatalf("findTransitiveDependents(a) = %v, want b, c, d", got)
	}
	for _, id := range got {
		if !want[id] {
			t.Errorf("unexpected step %q in transitive dependents", id)
		}
	}
}
