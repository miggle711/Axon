package engine

import (
	"context"
	"encoding/json"
	"fmt"
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
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{})

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

	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry)

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

func TestAgentCall_ErrorCases(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	agentCallStep := func() []StepDefinition {
		return []StepDefinition{
			{ID: "call_child", Type: StepTypeAgentCall, Agent: "child_agent", DependsOn: []string{}},
		}
	}

	t.Run("unknown agent", func(t *testing.T) {
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), MapAgentRegistry{})
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
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), registry)
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
		orchestrator := NewOrchestrator(newFakeRunStore(), NewQueueClient(server.URL), registry)
		_, err := orchestrator.CreateRun(context.Background(), AgentDefinition{Name: "parent", Steps: agentCallStep()}, "start")
		if err == nil {
			t.Fatal("expected an error for an output_step that doesn't match any step ID, got none")
		}
	})
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
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{})

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
}

// TestSupervisor_IterationCap verifies that hitting
// MaxSupervisorIterations force-stops the loop (ignoring a further
// Options pick) using the most recent option's output, rather than
// enqueuing a 6th option.
func TestSupervisor_IterationCap(t *testing.T) {
	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{})

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
		orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{})
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
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), MapAgentRegistry{})

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
