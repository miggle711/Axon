package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"sync"
	"testing"
)

// discardLogger silences log output for tests that don't assert on it.
var discardLogger = slog.New(slog.DiscardHandler)

// fakeRunStore is an in-memory RunStore for testing, avoiding a real
// Redis dependency.
type fakeRunStore struct {
	mu   sync.Mutex
	runs map[string]*Run
}

func newFakeRunStore() *fakeRunStore {
	return &fakeRunStore{runs: make(map[string]*Run)}
}

// SaveRun/GetRun round-trip through JSON, same as RedisRunStore, so
// each GetRun returns an independent copy — matching real behavior
// closely enough to reproduce the read-modify-write race a shared
// pointer would mask.
func (s *fakeRunStore) SaveRun(ctx context.Context, run *Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.runs[run.ID] = &Run{}
	return json.Unmarshal(data, s.runs[run.ID])
}

func (s *fakeRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	s.mu.Lock()
	stored, ok := s.runs[runID]
	s.mu.Unlock()
	if !ok {
		return nil, nil
	}

	data, err := json.Marshal(stored)
	if err != nil {
		return nil, err
	}
	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

// ListRuns mirrors RedisRunStore.ListRuns' semantics (top-level runs
// only, newest first, filtered and paginated the same way) without
// needing a real Redis sorted-set index, since correctness here is
// about the filter/sort/paginate logic itself, not the storage engine.
func (s *fakeRunStore) ListRuns(ctx context.Context, opts ListRunsOptions) ([]*Run, error) {
	s.mu.Lock()
	defer s.mu.Unlock()

	var matched []*Run
	for _, run := range s.runs {
		if run.ParentRunID != "" {
			continue
		}
		if opts.AgentName != "" && run.AgentName != opts.AgentName {
			continue
		}
		if opts.Status != "" && run.Status != opts.Status {
			continue
		}
		matched = append(matched, run)
	}

	sort.Slice(matched, func(i, j int) bool {
		return matched[i].CreatedAt.After(matched[j].CreatedAt)
	})

	limit := opts.Limit
	if limit <= 0 || limit > maxListRunsLimit {
		limit = defaultListRunsLimit
	}
	start := opts.Offset
	if start < 0 {
		start = 0
	}
	if start > len(matched) {
		start = len(matched)
	}
	end := start + limit
	if end > len(matched) {
		end = len(matched)
	}

	result := make([]*Run, 0, end-start)
	for _, run := range matched[start:end] {
		data, err := json.Marshal(run)
		if err != nil {
			return nil, err
		}
		var copyRun Run
		if err := json.Unmarshal(data, &copyRun); err != nil {
			return nil, err
		}
		result = append(result, &copyRun)
	}
	return result, nil
}

// newFakeQueueServer stands in for the queue service's POST /jobs
// endpoint, always returning a fresh job ID with 201.
func newFakeQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	counter := 0
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		counter++
		w.WriteHeader(http.StatusCreated)
		_ = json.NewEncoder(w).Encode(map[string]string{"id": "job-" + string(rune('0'+counter))})
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
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

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

// TestAgentCall_SpawnsChildRunAndPropagatesCompletion covers #16's
// "done when" criteria for agent_call: a parent run with an agent_call
// step spawns a child run, and the child's completion correctly
// unblocks the parent's next step.
// TestRunEvents_RecordedInOrder covers #38: Run.Events should record
// this run's meaningful transitions in the order they actually
// happened, not just the final snapshot the rest of Run already
// gives. Uses the same conditional_test_agent shape as
// TestOnStepCompleted_ConditionalRouting, since that test already
// establishes what each step does.
func TestRunEvents_RecordedInOrder(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	agent := AgentDefinition{
		Name:  "conditional_test_agent",
		Steps: conditionalTestAgentSteps(),
	}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if len(run.Events) != 2 {
		t.Fatalf("expected 2 events after CreateRun (run_created, step_enqueued), got %d: %+v", len(run.Events), run.Events)
	}
	if run.Events[0].Type != "run_created" {
		t.Errorf("expected first event to be run_created, got %q", run.Events[0].Type)
	}
	if run.Events[1].Type != "step_enqueued" || run.Events[1].StepID != "step_1" {
		t.Errorf("expected second event to be step_enqueued for step_1, got %+v", run.Events[1])
	}

	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "step_1", Output: "success"}); err != nil {
		t.Fatalf("OnStepCompleted(step_1) failed: %v", err)
	}
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "on_true_step", Output: "took the true branch"}); err != nil {
		t.Fatalf("OnStepCompleted(on_true_step) failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	wantTypes := []string{
		"run_created",
		"step_enqueued",       // step_1
		"step_completed",      // step_1
		"condition_evaluated", // check
		"step_enqueued",       // on_true_step
		"step_completed",      // on_true_step
		"run_completed",
	}
	if len(run.Events) != len(wantTypes) {
		t.Fatalf("expected %d events, got %d: %+v", len(wantTypes), len(run.Events), run.Events)
	}
	for i, wantType := range wantTypes {
		if run.Events[i].Type != wantType {
			t.Errorf("event %d: expected type %q, got %q", i, wantType, run.Events[i].Type)
		}
		if run.Events[i].Timestamp.IsZero() {
			t.Errorf("event %d: expected a non-zero timestamp", i)
		}
	}

	// Timestamps should be non-decreasing in the order events were
	// recorded, since that ordering is the entire point of this field.
	for i := 1; i < len(run.Events); i++ {
		if run.Events[i].Timestamp.Before(run.Events[i-1].Timestamp) {
			t.Errorf("event %d (%s) has an earlier timestamp than event %d (%s)", i, run.Events[i].Type, i-1, run.Events[i-1].Type)
		}
	}
}

func TestAgentCall_SpawnsChildRunAndPropagatesCompletion(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()

	childAgent := AgentDefinition{
		Name:       "child_agent",
		OutputStep: "child_step",
		Steps: []StepDefinition{
			{ID: "child_step", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "child output", DependsOn: []string{}},
		},
	}
	registry := MapAgentRegistry{"child_agent": childAgent}

	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry, discardLogger)

	parentAgent := AgentDefinition{
		Name: "parent_agent",
		Steps: []StepDefinition{
			{ID: "call_child", Type: StepTypeAgentCall, Agent: "child_agent", DependsOn: []string{}},
			{ID: "after_child", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "{{call_child.output}}", DependsOn: []string{"call_child"}},
		},
	}

	ctx := context.Background()
	parentRun, err := orchestrator.CreateRun(ctx, parentAgent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	enqueuedRef, ok := parentRun.EnqueuedSteps["call_child"]
	if !ok || len(enqueuedRef) < 4 || enqueuedRef[:4] != "run:" {
		t.Fatalf("expected call_child to be marked with a child run reference, got %q", enqueuedRef)
	}
	childRunID := enqueuedRef[4:]

	childRun, err := orchestrator.GetRun(ctx, childRunID)
	if err != nil {
		t.Fatalf("GetRun(child) failed: %v", err)
	}
	if childRun == nil {
		t.Fatal("expected child run to exist in the store")
	}
	if childRun.ParentRunID != parentRun.ID || childRun.ParentStepID != "call_child" {
		t.Errorf("expected child run's parent fields to point back to the parent, got ParentRunID=%q ParentStepID=%q",
			childRun.ParentRunID, childRun.ParentStepID)
	}
	if _, enqueued := childRun.EnqueuedSteps["child_step"]; !enqueued {
		t.Fatalf("expected child_step to be enqueued in the child run, got EnqueuedSteps=%v", childRun.EnqueuedSteps)
	}

	// Simulate the worker completing the child run's only step.
	err = orchestrator.OnStepCompleted(ctx, WebhookPayload{
		RunID:  childRun.ID,
		StepID: "child_step",
		Output: "child output",
	})
	if err != nil {
		t.Fatalf("OnStepCompleted(child_step) failed: %v", err)
	}

	childRun, err = orchestrator.GetRun(ctx, childRun.ID)
	if err != nil {
		t.Fatalf("GetRun(child) failed: %v", err)
	}
	if childRun.Status != "completed" {
		t.Fatalf("expected child run to be completed, got %q", childRun.Status)
	}

	// The child's completion should have propagated back to the parent,
	// resolving call_child's result and enqueuing after_child.
	parentRun, err = orchestrator.GetRun(ctx, parentRun.ID)
	if err != nil {
		t.Fatalf("GetRun(parent) failed: %v", err)
	}
	if parentRun.StepResults["call_child"] != "child output" {
		t.Errorf("expected call_child result 'child output', got %q", parentRun.StepResults["call_child"])
	}
	if _, enqueued := parentRun.EnqueuedSteps["after_child"]; !enqueued {
		t.Errorf("expected after_child to be enqueued after child run completion, got EnqueuedSteps=%v", parentRun.EnqueuedSteps)
	}
}

// TestAgentCall_OutputStepNamingConditional covers #56's fourth
// constraint: a sub-agent whose OutputStep names a conditional step
// directly, rather than one of its branches. Before the fix, the
// parent got back the winning branch's literal step ID string (e.g.
// "on_true_step") instead of that branch's actual output, since a
// conditional's own StepResults entry holds the winner's step ID, not
// its output - confirmed live against conditional_subagent.json/
// conditional_subagent_caller.json before being fixed here.
func TestAgentCall_OutputStepNamingConditional(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()

	childAgent := AgentDefinition{
		Name:       "child_agent",
		OutputStep: "check",
		Steps:      conditionalTestAgentSteps(),
	}
	registry := MapAgentRegistry{"child_agent": childAgent}

	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry, discardLogger)

	parentAgent := AgentDefinition{
		Name: "parent_agent",
		Steps: []StepDefinition{
			{ID: "call_child", Type: StepTypeAgentCall, Agent: "child_agent", DependsOn: []string{}},
		},
	}

	ctx := context.Background()
	parentRun, err := orchestrator.CreateRun(ctx, parentAgent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	enqueuedRef := parentRun.EnqueuedSteps["call_child"]
	childRunID := enqueuedRef[len("run:"):]

	// Drive the child run's steps the same way TestOnStepCompleted_ConditionalRouting
	// does: step_1 completes with "success", routing check to on_true_step.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: childRunID, StepID: "step_1", Output: "success"}); err != nil {
		t.Fatalf("OnStepCompleted(step_1) failed: %v", err)
	}
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: childRunID, StepID: "on_true_step", Output: "the real on_true_step output"}); err != nil {
		t.Fatalf("OnStepCompleted(on_true_step) failed: %v", err)
	}

	childRun, err := orchestrator.GetRun(ctx, childRunID)
	if err != nil {
		t.Fatalf("GetRun(child) failed: %v", err)
	}
	if childRun.Status != "completed" {
		t.Fatalf("expected child run to be completed, got %q", childRun.Status)
	}

	parentRun, err = orchestrator.GetRun(ctx, parentRun.ID)
	if err != nil {
		t.Fatalf("GetRun(parent) failed: %v", err)
	}
	if parentRun.StepResults["call_child"] != "the real on_true_step output" {
		t.Errorf("expected call_child result to be on_true_step's real output, got %q", parentRun.StepResults["call_child"])
	}
}

func TestAgentCall_ErrorCases(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	agentCallStep := func() []StepDefinition {
		return []StepDefinition{
			{ID: "call_child", Type: StepTypeAgentCall, Agent: "child_agent", DependsOn: []string{}},
		}
	}

	t.Run("unknown agent", func(t *testing.T) {
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)
		_, err := orchestrator.CreateRun(context.Background(), AgentDefinition{Name: "parent", Steps: agentCallStep()}, "start")
		if err == nil {
			t.Fatal("expected an error for an unregistered agent, got none")
		}
	})

	t.Run("missing output_step", func(t *testing.T) {
		registry := MapAgentRegistry{"child_agent": AgentDefinition{
			Name:  "child_agent",
			Steps: []StepDefinition{{ID: "child_step", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "x", DependsOn: []string{}}},
			// OutputStep intentionally unset
		}}
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), registry, discardLogger)
		_, err := orchestrator.CreateRun(context.Background(), AgentDefinition{Name: "parent", Steps: agentCallStep()}, "start")
		if err == nil {
			t.Fatal("expected an error for a child agent with no output_step, got none")
		}
	})

	t.Run("output_step does not match any step ID", func(t *testing.T) {
		registry := MapAgentRegistry{"child_agent": AgentDefinition{
			Name:       "child_agent",
			OutputStep: "does_not_exist",
			Steps:      []StepDefinition{{ID: "child_step", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "x", DependsOn: []string{}}},
		}}
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), registry, discardLogger)
		_, err := orchestrator.CreateRun(context.Background(), AgentDefinition{Name: "parent", Steps: agentCallStep()}, "start")
		if err == nil {
			t.Fatal("expected an error for an output_step that doesn't match any step ID, got none")
		}
	})
}

// TestCreateRunByName covers #40's need for the CLI (or any caller
// without its own access to agent definitions) to start a run by name
// alone, resolving via the orchestrator's own AgentRegistry.
func TestCreateRunByName(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	def := AgentDefinition{
		Name:  "greeter",
		Steps: []StepDefinition{{ID: "greet", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "hi {{user_input}}", DependsOn: []string{}}},
	}
	registry := MapAgentRegistry{"greeter": def}
	orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), registry, discardLogger)

	run, err := orchestrator.CreateRunByName(context.Background(), "greeter", "world")
	if err != nil {
		t.Fatalf("CreateRunByName failed: %v", err)
	}
	if run.AgentName != "greeter" {
		t.Errorf("expected AgentName 'greeter', got %q", run.AgentName)
	}
	if run.UserInput != "world" {
		t.Errorf("expected UserInput 'world', got %q", run.UserInput)
	}
	if _, enqueued := run.EnqueuedSteps["greet"]; !enqueued {
		t.Errorf("expected greet to be enqueued, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
}

func TestCreateRunByName_UnknownAgent(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)
	_, err := orchestrator.CreateRunByName(context.Background(), "does_not_exist", "input")
	if err == nil {
		t.Fatal("expected an error for an unregistered agent name, got none")
	}
}

// supervisorTestAgentSteps builds a supervisor step with two Options,
// each a tool_call that depends on the supervisor and produces a
// distinguishable output, plus a final step depending on the
// supervisor's own eventual result.
func supervisorTestAgentSteps() []StepDefinition {
	return []StepDefinition{
		{
			ID:             "supervisor_step",
			Type:           StepTypeSupervisor,
			PromptTemplate: "decide",
			Options:        []string{"option_a", "option_b"},
			DependsOn:      []string{},
		},
		{
			ID:            "option_a",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "ran option a",
			DependsOn:     []string{"supervisor_step"},
		},
		{
			ID:            "option_b",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "ran option b",
			DependsOn:     []string{"supervisor_step"},
		},
		{
			ID:            "after_supervisor",
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "{{supervisor_step.output}}",
			DependsOn:     []string{"supervisor_step"},
		},
	}
}

// TestSupervisor_LoopsAndStops exercises the full supervisor loop: it
// picks an option, the option completes and re-triggers the supervisor
// for another decision, and it eventually stops on the "done" signal —
// unblocking after_supervisor with the last chosen option's output.
func TestSupervisor_LoopsAndStops(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	agent := AgentDefinition{Name: "supervisor_test_agent", Steps: supervisorTestAgentSteps()}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if _, enqueued := run.EnqueuedSteps["supervisor_step"]; !enqueued {
		t.Fatalf("expected supervisor_step to be enqueued after CreateRun, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Iteration 1: supervisor picks option_a.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_a"}); err != nil {
		t.Fatalf("supervisor decision 1 (option_a) failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.ActiveSupervisorChoice["supervisor_step"] != "option_a" {
		t.Fatalf("expected active choice option_a, got %q", run.ActiveSupervisorChoice["supervisor_step"])
	}
	if run.SupervisorIterations["supervisor_step"] != 1 {
		t.Fatalf("expected 1 iteration recorded, got %d", run.SupervisorIterations["supervisor_step"])
	}
	if _, enqueued := run.EnqueuedSteps["option_a"]; !enqueued {
		t.Fatalf("expected option_a to be enqueued, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// option_a completes, which should re-trigger the supervisor rather
	// than treating option_a as a normally-completed step.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "option_a", Output: "ran option a"}); err != nil {
		t.Fatalf("option_a completion failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	for _, completedID := range run.CompletedSteps {
		if completedID == "option_a" {
			t.Errorf("option_a should not be in CompletedSteps while its supervisor is still looping, got %v", run.CompletedSteps)
		}
	}
	if _, enqueued := run.EnqueuedSteps["supervisor_step"]; !enqueued {
		t.Fatalf("expected supervisor_step to be re-enqueued for its next decision, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Iteration 2: supervisor picks option_b this time.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_b"}); err != nil {
		t.Fatalf("supervisor decision 2 (option_b) failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.SupervisorIterations["supervisor_step"] != 2 {
		t.Fatalf("expected 2 iterations recorded, got %d", run.SupervisorIterations["supervisor_step"])
	}

	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "option_b", Output: "ran option b"}); err != nil {
		t.Fatalf("option_b completion failed: %v", err)
	}

	// Iteration 3: supervisor says done.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: SupervisorDoneSignal}); err != nil {
		t.Fatalf("supervisor decision 3 (done) failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if _, active := run.ActiveSupervisorChoice["supervisor_step"]; active {
		t.Errorf("expected no active choice after stopping, got %v", run.ActiveSupervisorChoice)
	}
	if run.StepResults["supervisor_step"] != "ran option b" {
		t.Errorf("expected supervisor_step result to be the last chosen option's output 'ran option b', got %q", run.StepResults["supervisor_step"])
	}
	found := false
	for _, completedID := range run.CompletedSteps {
		if completedID == "supervisor_step" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected supervisor_step in CompletedSteps once stopped, got %v", run.CompletedSteps)
	}
	if _, enqueued := run.EnqueuedSteps["after_supervisor"]; !enqueued {
		t.Errorf("expected after_supervisor to be enqueued once the supervisor stopped, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Completing the one step left (after_supervisor) should bring the
	// run to Status "completed" - regression coverage for #57: the old
	// completion formula could never be satisfied for any agent with a
	// supervisor loop, since option_a/option_b (the supervisor's own
	// Options) never get their own CompletedSteps entry by design.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "after_supervisor", Output: "done"}); err != nil {
		t.Fatalf("after_supervisor completion failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.Status != "completed" {
		t.Errorf("expected run Status 'completed' once every non-option step is done, got %q (completed=%v skipped=%v steps=%d)",
			run.Status, run.CompletedSteps, run.SkippedSteps, len(run.Steps))
	}
}

// TestSupervisor_FirstIterationStep covers #56's structural fix: a
// supervisor with FirstIterationStep set skips the LLM decision call
// entirely on its first iteration, auto-picking that option instead,
// then goes back to normal LLM-driven decisions from the second
// iteration on.
func TestSupervisor_FirstIterationStep(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	steps := supervisorTestAgentSteps()
	for i := range steps {
		if steps[i].ID == "supervisor_step" {
			steps[i].FirstIterationStep = "option_a"
		}
	}
	agent := AgentDefinition{Name: "supervisor_first_iteration_test_agent", Steps: steps}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	// option_a should be enqueued directly, no LLM decision job created
	// for the supervisor step itself on this first iteration - its
	// EnqueuedSteps entry is a marker ("auto:option_a"), not a real job ID.
	if got := run.EnqueuedSteps["supervisor_step"]; got != "auto:option_a" {
		t.Fatalf("expected supervisor_step's EnqueuedSteps entry to be the auto-pick marker \"auto:option_a\" (no real LLM job), got %q; EnqueuedSteps=%v", got, run.EnqueuedSteps)
	}
	if _, enqueued := run.EnqueuedSteps["option_a"]; !enqueued {
		t.Fatalf("expected option_a to be auto-enqueued as the first_iteration_step, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
	if run.ActiveSupervisorChoice["supervisor_step"] != "option_a" {
		t.Fatalf("expected active choice option_a, got %q", run.ActiveSupervisorChoice["supervisor_step"])
	}
	if run.SupervisorIterations["supervisor_step"] != 1 {
		t.Fatalf("expected 1 iteration recorded after the auto-pick, got %d", run.SupervisorIterations["supervisor_step"])
	}

	// option_a completes, which should now re-invoke the supervisor as
	// a normal LLM decision call, since the first iteration is done.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "option_a", Output: "ran option a"}); err != nil {
		t.Fatalf("option_a completion failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if _, enqueued := run.EnqueuedSteps["supervisor_step"]; !enqueued {
		t.Fatalf("expected supervisor_step to be enqueued as a normal LLM decision on iteration 2, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Iteration 2: supervisor says done, using option_a's output since
	// no second option ran.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: SupervisorDoneSignal}); err != nil {
		t.Fatalf("supervisor decision (done) failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.StepResults["supervisor_step"] != "ran option a" {
		t.Errorf("expected supervisor_step result 'ran option a', got %q", run.StepResults["supervisor_step"])
	}
}

// TestSupervisor_IterationCap verifies that hitting
// MaxSupervisorIterations force-stops the loop (ignoring a further
// Options pick) using the most recent option's output, rather than
// enqueuing a 6th option.
func TestSupervisor_IterationCap(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	agent := AgentDefinition{Name: "supervisor_test_agent", Steps: supervisorTestAgentSteps()}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	for i := 0; i < MaxSupervisorIterations; i++ {
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_a"}); err != nil {
			t.Fatalf("supervisor decision %d failed: %v", i+1, err)
		}
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "option_a", Output: fmt.Sprintf("ran option a #%d", i+1)}); err != nil {
			t.Fatalf("option_a completion %d failed: %v", i+1, err)
		}
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.SupervisorIterations["supervisor_step"] != MaxSupervisorIterations {
		t.Fatalf("expected %d iterations recorded, got %d", MaxSupervisorIterations, run.SupervisorIterations["supervisor_step"])
	}

	// A 6th pick should be force-stopped instead of enqueued.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_a"}); err != nil {
		t.Fatalf("supervisor decision at cap failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.SupervisorIterations["supervisor_step"] != MaxSupervisorIterations {
		t.Errorf("expected iteration count to stay at the cap %d, got %d", MaxSupervisorIterations, run.SupervisorIterations["supervisor_step"])
	}
	wantOutput := fmt.Sprintf("ran option a #%d", MaxSupervisorIterations)
	if run.StepResults["supervisor_step"] != wantOutput {
		t.Errorf("expected supervisor_step result %q (the capped iteration's output), got %q", wantOutput, run.StepResults["supervisor_step"])
	}
}

func TestSupervisor_ErrorCases(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	newOrchestrator := func() (*Orchestrator, *Run) {
		store := newFakeRunStore()
		orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)
		agent := AgentDefinition{Name: "supervisor_test_agent", Steps: supervisorTestAgentSteps()}
		run, err := orchestrator.CreateRun(context.Background(), agent, "start")
		if err != nil {
			t.Fatalf("CreateRun failed: %v", err)
		}
		return orchestrator, run
	}

	t.Run("unrecognized output", func(t *testing.T) {
		orchestrator, run := newOrchestrator()
		err := orchestrator.OnStepCompleted(context.Background(), WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "not a real option"})
		if err == nil {
			t.Fatal("expected an error for an unrecognized supervisor output, got none")
		}
	})

	t.Run("done before any option ran", func(t *testing.T) {
		orchestrator, run := newOrchestrator()
		err := orchestrator.OnStepCompleted(context.Background(), WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: SupervisorDoneSignal})
		if err == nil {
			t.Fatal("expected an error for 'done' before any option has run, got none")
		}
	})
}

// TestOnStepFailed_MarksRunFailed covers #47: a permanently failed
// step must fail the run outright (a new terminal Status, not left at
// "in_progress" forever) and record the step in FailedSteps.
func TestOnStepFailed_MarksRunFailed(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	agent := AgentDefinition{
		Name: "test_agent",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "x", DependsOn: []string{}},
		},
	}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	err = orchestrator.OnStepFailed(ctx, WebhookFailedPayload{RunID: run.ID, StepID: "step_1", Reason: "rate limited"})
	if err != nil {
		t.Fatalf("OnStepFailed failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.Status != "failed" {
		t.Errorf("expected Status 'failed', got %q", run.Status)
	}
	found := false
	for _, id := range run.FailedSteps {
		if id == "step_1" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected step_1 in FailedSteps, got %v", run.FailedSteps)
	}
}

// TestOnStepFailed_ClearsActiveSupervisorChoice covers #47's
// supervisor-specific case: a permanently failed supervisor-owned step
// (its own decision, or the option it's waiting on) must not leave
// ActiveSupervisorChoice dangling forever.
func TestOnStepFailed_ClearsActiveSupervisorChoice(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	t.Run("supervisor's own decision fails", func(t *testing.T) {
		store := newFakeRunStore()
		orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)
		agent := AgentDefinition{Name: "supervisor_test_agent", Steps: supervisorTestAgentSteps()}

		ctx := context.Background()
		run, err := orchestrator.CreateRun(ctx, agent, "start")
		if err != nil {
			t.Fatalf("CreateRun failed: %v", err)
		}
		// Get the loop started so there's an ActiveSupervisorChoice entry to clear.
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_a"}); err != nil {
			t.Fatalf("OnStepCompleted(supervisor_step) failed: %v", err)
		}

		// The supervisor's own next decision job fails permanently -
		// e.g. option_a completed and re-triggered the supervisor, and
		// this new decision job never comes back.
		if err := orchestrator.OnStepFailed(ctx, WebhookFailedPayload{RunID: run.ID, StepID: "supervisor_step", Reason: "rate limited"}); err != nil {
			t.Fatalf("OnStepFailed failed: %v", err)
		}

		run, err = orchestrator.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun failed: %v", err)
		}
		if run.Status != "failed" {
			t.Errorf("expected Status 'failed', got %q", run.Status)
		}
		if _, active := run.ActiveSupervisorChoice["supervisor_step"]; active {
			t.Errorf("expected ActiveSupervisorChoice to be cleared, got %v", run.ActiveSupervisorChoice)
		}
	})

	t.Run("supervisor's chosen option fails", func(t *testing.T) {
		store := newFakeRunStore()
		orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)
		agent := AgentDefinition{Name: "supervisor_test_agent", Steps: supervisorTestAgentSteps()}

		ctx := context.Background()
		run, err := orchestrator.CreateRun(ctx, agent, "start")
		if err != nil {
			t.Fatalf("CreateRun failed: %v", err)
		}
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "supervisor_step", Output: "option_a"}); err != nil {
			t.Fatalf("OnStepCompleted(supervisor_step) failed: %v", err)
		}

		// option_a, the step the supervisor is currently waiting on, fails permanently.
		if err := orchestrator.OnStepFailed(ctx, WebhookFailedPayload{RunID: run.ID, StepID: "option_a", Reason: "rate limited"}); err != nil {
			t.Fatalf("OnStepFailed failed: %v", err)
		}

		run, err = orchestrator.GetRun(ctx, run.ID)
		if err != nil {
			t.Fatalf("GetRun failed: %v", err)
		}
		if run.Status != "failed" {
			t.Errorf("expected Status 'failed', got %q", run.Status)
		}
		if _, active := run.ActiveSupervisorChoice["supervisor_step"]; active {
			t.Errorf("expected ActiveSupervisorChoice to be cleared, got %v", run.ActiveSupervisorChoice)
		}
	})
}

// TestOnStepFailed_PropagatesToParent covers #47: a child run spawned
// via agent_call failing must fail the parent's agent_call step too,
// mirroring how a successful completion already propagates.
func TestOnStepFailed_PropagatesToParent(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()

	childAgent := AgentDefinition{
		Name:       "child_agent",
		OutputStep: "child_step",
		Steps: []StepDefinition{
			{ID: "child_step", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "x", DependsOn: []string{}},
		},
	}
	registry := MapAgentRegistry{"child_agent": childAgent}
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry, discardLogger)

	parentAgent := AgentDefinition{
		Name: "parent_agent",
		Steps: []StepDefinition{
			{ID: "call_child", Type: StepTypeAgentCall, Agent: "child_agent", DependsOn: []string{}},
		},
	}

	ctx := context.Background()
	parentRun, err := orchestrator.CreateRun(ctx, parentAgent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	enqueuedRef := parentRun.EnqueuedSteps["call_child"]
	childRunID := enqueuedRef[len("run:"):]

	// The child run's only step fails permanently.
	if err := orchestrator.OnStepFailed(ctx, WebhookFailedPayload{RunID: childRunID, StepID: "child_step", Reason: "rate limited"}); err != nil {
		t.Fatalf("OnStepFailed(child) failed: %v", err)
	}

	childRun, err := orchestrator.GetRun(ctx, childRunID)
	if err != nil {
		t.Fatalf("GetRun(child) failed: %v", err)
	}
	if childRun.Status != "failed" {
		t.Errorf("expected child run Status 'failed', got %q", childRun.Status)
	}

	parentRun, err = orchestrator.GetRun(ctx, parentRun.ID)
	if err != nil {
		t.Fatalf("GetRun(parent) failed: %v", err)
	}
	if parentRun.Status != "failed" {
		t.Errorf("expected parent run Status 'failed', got %q", parentRun.Status)
	}
	found := false
	for _, id := range parentRun.FailedSteps {
		if id == "call_child" {
			found = true
		}
	}
	if !found {
		t.Errorf("expected call_child in parent's FailedSteps, got %v", parentRun.FailedSteps)
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
		{"contains true", "{{step_1.output}} contains succ", true, false},
		{"contains false", "{{step_1.output}} contains fail", false, false},
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

// TestEvaluateCondition_FreeFormLLMOutput demonstrates the motivating case
// from #25: == is brittle against free-form llm_call output, contains is not.
func TestEvaluateCondition_FreeFormLLMOutput(t *testing.T) {
	run := &Run{
		StepResults: map[string]string{"llm_step": "Yes, that worked."},
	}

	got, err := evaluateCondition("{{llm_step.output}} == success", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("exact match unexpectedly succeeded against free-form LLM phrasing")
	}

	got, err = evaluateCondition("{{llm_step.output}} contains worked", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Error("contains should match a substring of free-form LLM phrasing")
	}
}

// TestEvaluateCondition_OperatorInResolvedValue is the regression case from
// #30: an operator-like substring inside the *resolved* value (e.g. llm_call
// output containing "!=") must not be mistaken for the condition's actual
// operator. The operator must be found in the unresolved condition string.
func TestEvaluateCondition_OperatorInResolvedValue(t *testing.T) {
	run := &Run{
		StepResults: map[string]string{"step": "the tool failed, output != expected"},
	}

	got, err := evaluateCondition("{{step.output}} contains worked", run)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Error("expected false: resolved value has no \"worked\" substring, but a stray \"!=\" in the value must not hijack the split")
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

// TestOnStepCompleted_ConcurrentCallsDoNotLoseUpdates reproduces
// issue #19: two OnStepCompleted calls for the same run, fired
// concurrently, must not clobber each other's StepResults update via
// an unsynchronized read-modify-write.
func TestOnStepCompleted_ConcurrentCallsDoNotLoseUpdates(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

	agent := AgentDefinition{
		Name: "concurrent_test_agent",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{}},
			{ID: "step_2", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{}},
		},
	}

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, agent, "start")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{
			RunID: run.ID, StepID: "step_1", Output: "result_1",
		}); err != nil {
			t.Errorf("OnStepCompleted(step_1) failed: %v", err)
		}
	}()
	go func() {
		defer wg.Done()
		if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{
			RunID: run.ID, StepID: "step_2", Output: "result_2",
		}); err != nil {
			t.Errorf("OnStepCompleted(step_2) failed: %v", err)
		}
	}()
	wg.Wait()

	final, err := orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}

	if final.StepResults["step_1"] != "result_1" {
		t.Errorf("step_1 result lost: got %q, want %q", final.StepResults["step_1"], "result_1")
	}
	if final.StepResults["step_2"] != "result_2" {
		t.Errorf("step_2 result lost: got %q, want %q", final.StepResults["step_2"], "result_2")
	}
	if final.Status != "completed" {
		t.Errorf("expected run status 'completed', got %q (completed=%v)", final.Status, final.CompletedSteps)
	}
}

// TestRunIsComplete covers #57: the run-completion check must count a
// supervisor's Options steps as accounted for once the supervisor
// itself has completed, since an Options step never gets its own
// CompletedSteps entry (see canEnqueueStep's comment) no matter how
// many times it actually ran.
func TestRunIsComplete(t *testing.T) {
	steps := supervisorTestAgentSteps() // supervisor_step, option_a, option_b, after_supervisor

	t.Run("false when a non-option step hasn't completed", func(t *testing.T) {
		run := &Run{Steps: steps, CompletedSteps: []string{"supervisor_step"}}
		if runIsComplete(run) {
			t.Error("expected incomplete: after_supervisor hasn't run yet")
		}
	})

	t.Run("true once every step except the supervisor's own options is done, even though the options themselves never appear in CompletedSteps", func(t *testing.T) {
		run := &Run{Steps: steps, CompletedSteps: []string{"supervisor_step", "after_supervisor"}}
		if !runIsComplete(run) {
			t.Error("expected complete: option_a/option_b are the supervisor's own options and don't need their own CompletedSteps entry")
		}
	})

	t.Run("skipped steps count toward completion same as completed ones", func(t *testing.T) {
		def := AgentDefinition{Steps: conditionalTestAgentSteps()} // step_1, check, on_true_step, on_false_step
		run := &Run{
			Steps:          def.Steps,
			CompletedSteps: []string{"step_1", "check", "on_true_step"},
			SkippedSteps:   []string{"on_false_step"},
		}
		if !runIsComplete(run) {
			t.Error("expected complete: on_false_step is skipped, not completed, but should still count")
		}
	})

	t.Run("an agent with no supervisor steps behaves exactly as before", func(t *testing.T) {
		run := &Run{
			Steps:          []StepDefinition{{ID: "step_1", Type: StepTypeToolCall}},
			CompletedSteps: []string{"step_1"},
		}
		if !runIsComplete(run) {
			t.Error("expected complete: the one step is done and there's no supervisor involved")
		}
	})
}

func TestResolveStepInput(t *testing.T) {
	run := &Run{
		UserInput:   "hello",
		StepResults: map[string]string{"step_1": "success"},
	}

	toolCallStep := StepDefinition{
		ID:             "tool_step",
		Type:           StepTypeToolCall,
		InputTemplate:  "input says {{user_input}}",
		PromptTemplate: "prompt says {{user_input}}",
	}
	if got, want := resolveStepInput(toolCallStep, run), "input says hello"; got != want {
		t.Errorf("tool_call: resolveStepInput() = %q, want %q", got, want)
	}

	llmCallStep := StepDefinition{
		ID:             "llm_step",
		Type:           StepTypeLLMCall,
		InputTemplate:  "input says {{user_input}}",
		PromptTemplate: "prompt says {{step_1.output}}",
	}
	if got, want := resolveStepInput(llmCallStep, run), "prompt says success"; got != want {
		t.Errorf("llm_call: resolveStepInput() = %q, want %q", got, want)
	}

	// An llm_call step with no InputTemplate set (the realistic case
	// before this fix, InputTemplate would resolve to "" and silently
	// send an empty prompt) must still resolve PromptTemplate.
	llmCallNoInput := StepDefinition{
		ID:             "llm_step_2",
		Type:           StepTypeLLMCall,
		PromptTemplate: "summarize {{step_1.output}}",
	}
	if got, want := resolveStepInput(llmCallNoInput, run), "summarize success"; got != want {
		t.Errorf("llm_call with no InputTemplate: resolveStepInput() = %q, want %q", got, want)
	}
}

// TestResolveTemplate_IterationPlaceholder covers #56's first fix: a
// supervisor's own prompt can reference {{step_id.iteration}} to know
// how many times it's already looped, instead of the prompt text
// having to instruct the model to infer that itself.
func TestResolveTemplate_IterationPlaceholder(t *testing.T) {
	t.Run("resolves to 0 before any decision has been made", func(t *testing.T) {
		// SupervisorIterations has no entry for "judge" yet - this is
		// the exact case the fix targets: the very first time a
		// supervisor's prompt is resolved, before handleSupervisorDecision
		// has ever incremented the map for it.
		run := &Run{
			Steps:                []StepDefinition{{ID: "judge", Type: StepTypeSupervisor}},
			SupervisorIterations: map[string]int{},
		}
		got := resolveTemplate("iteration: {{judge.iteration}}", run)
		if got != "iteration: 0" {
			t.Errorf("got %q, want %q", got, "iteration: 0")
		}
	})

	t.Run("resolves to the current count after decisions have been made", func(t *testing.T) {
		run := &Run{
			Steps:                []StepDefinition{{ID: "judge", Type: StepTypeSupervisor}},
			SupervisorIterations: map[string]int{"judge": 3},
		}
		got := resolveTemplate("iteration: {{judge.iteration}}", run)
		if got != "iteration: 3" {
			t.Errorf("got %q, want %q", got, "iteration: 3")
		}
	})

	t.Run("a non-supervisor step referencing .iteration resolves to 0, not an error", func(t *testing.T) {
		run := &Run{
			Steps:                []StepDefinition{{ID: "step_1", Type: StepTypeToolCall}},
			SupervisorIterations: map[string]int{},
		}
		got := resolveTemplate("iteration: {{step_1.iteration}}", run)
		if got != "iteration: 0" {
			t.Errorf("got %q, want %q", got, "iteration: 0")
		}
	})
}

// linearChainSteps builds n tool_call steps, each depending on the one
// before it: step_0 -> step_1 -> ... -> step_(n-1). The simplest shape
// that still forces OnStepCompleted's resolve loop (#11) to do real
// work on every completion, without conditionals/supervisors/agent_call
// complicating what's being measured.
func linearChainSteps(n int) []StepDefinition {
	steps := make([]StepDefinition, n)
	for i := 0; i < n; i++ {
		deps := []string{}
		if i > 0 {
			deps = []string{fmt.Sprintf("step_%d", i-1)}
		}
		steps[i] = StepDefinition{
			ID:            fmt.Sprintf("step_%d", i),
			Type:          StepTypeToolCall,
			Tool:          "echo",
			InputTemplate: "x",
			DependsOn:     deps,
		}
	}
	return steps
}

// BenchmarkOnStepCompleted_LinearChain measures the full, real,
// end-to-end cost of completing all n steps of a linear chain one at
// a time, the way a real worker webhook sequence would - including
// RunStore.GetRun/SaveRun on every call. Run with:
//
//	go test ./. -bench BenchmarkOnStepCompleted_LinearChain -benchtime=1x
//
// This still scales roughly O(n^2) even after #11's resolveSteps fix
// (see BenchmarkResolveSteps_LinearChain below for that fix measured
// in isolation), because GetRun/SaveRun each marshal/unmarshal the
// entire Run blob on every call - a separate O(n)-per-call cost
// (growing Steps/CompletedSteps/Events) that #11's fix never touched,
// filed as #81. This benchmark intentionally keeps measuring the real
// end-to-end path rather than hiding that behind a narrower one, since
// the whole-blob persistence cost is just as real as the resolve
// loop's was.
func BenchmarkOnStepCompleted_LinearChain(b *testing.B) {
	sizes := []int{50, 100, 200, 400}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "job"})
				}))

				store := newFakeRunStore()
				orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

				agent := AgentDefinition{Name: "linear_chain_agent", Steps: linearChainSteps(n)}
				ctx := context.Background()
				run, err := orchestrator.CreateRun(ctx, agent, "start")
				if err != nil {
					b.Fatalf("CreateRun failed: %v", err)
				}

				for j := 0; j < n; j++ {
					stepID := fmt.Sprintf("step_%d", j)
					if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: stepID, Output: "ok"}); err != nil {
						b.Fatalf("OnStepCompleted(%s) failed: %v", stepID, err)
					}
				}

				server.Close()
			}
		})
	}
}

// BenchmarkResolveSteps_LinearChain isolates #11's actual fix from the
// RunStore round trip #81 is about: it drives the same linear chain
// as BenchmarkOnStepCompleted_LinearChain, but completes each step by
// mutating run in memory directly and calling resolveSteps itself, the
// same way finalizeStepCompletion does internally, without ever going
// through GetRun/SaveRun. This is the benchmark that actually shows
// #11's dependents-index fix working: total time here should scale
// close to linearly in n, not quadratically.
func BenchmarkResolveSteps_LinearChain(b *testing.B) {
	sizes := []int{50, 100, 200, 400}
	for _, n := range sizes {
		b.Run(fmt.Sprintf("n=%d", n), func(b *testing.B) {
			for i := 0; i < b.N; i++ {
				server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]string{"id": "job"})
				}))

				orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), MapAgentRegistry{}, discardLogger)

				steps := linearChainSteps(n)
				run := &Run{
					ID:            "bench-run",
					AgentName:     "linear_chain_agent",
					Steps:         steps,
					StepResults:   make(map[string]string),
					EnqueuedSteps: make(map[string]string),
				}
				ctx := context.Background()
				if err := orchestrator.resolveSteps(ctx, run); err != nil {
					b.Fatalf("initial resolveSteps failed: %v", err)
				}

				for j := 0; j < n; j++ {
					stepID := fmt.Sprintf("step_%d", j)
					run.StepResults[stepID] = "ok"
					run.CompletedSteps = append(run.CompletedSteps, stepID)
					if err := orchestrator.resolveSteps(ctx, run); err != nil {
						b.Fatalf("resolveSteps(%s) failed: %v", stepID, err)
					}
				}

				server.Close()
			}
		})
	}
}
