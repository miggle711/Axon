package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestLoadAgentsFromDir(t *testing.T) {
	dir := t.TempDir()

	writeAgent(t, dir, "agent_one.json", `{"name": "agent_one", "steps": []}`)
	writeAgent(t, dir, "agent_two.json", `{"name": "agent_two", "output_step": "s1", "steps": [{"id": "s1", "type": "tool_call"}]}`)
	// Non-JSON files in the directory should be ignored, not error.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not an agent"), 0644); err != nil {
		t.Fatalf("failed to write README.md: %v", err)
	}

	registry, err := LoadAgentsFromDir(dir)
	if err != nil {
		t.Fatalf("LoadAgentsFromDir failed: %v", err)
	}

	if len(registry) != 2 {
		t.Fatalf("expected 2 agents loaded, got %d: %v", len(registry), registry)
	}

	agentOne, ok := registry.Get("agent_one")
	if !ok {
		t.Fatal("expected agent_one to be registered under its filename")
	}
	if agentOne.Name != "agent_one" {
		t.Errorf("expected agent_one's Name field to be 'agent_one', got %q", agentOne.Name)
	}

	agentTwo, ok := registry.Get("agent_two")
	if !ok {
		t.Fatal("expected agent_two to be registered under its filename")
	}
	if agentTwo.OutputStep != "s1" {
		t.Errorf("expected agent_two's OutputStep to be 's1', got %q", agentTwo.OutputStep)
	}
}

func TestLoadAgentsFromDir_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	writeAgent(t, dir, "broken.json", `{not valid json`)

	_, err := LoadAgentsFromDir(dir)
	if err == nil {
		t.Fatal("expected an error for a malformed agent file, got none")
	}
}

func TestLoadAgentsFromDir_MissingDir(t *testing.T) {
	_, err := LoadAgentsFromDir(filepath.Join(t.TempDir(), "does_not_exist"))
	if err == nil {
		t.Fatal("expected an error for a nonexistent directory, got none")
	}
}

func writeAgent(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

// TestAgentsDir_GreeterCallerEndToEnd loads the real agents committed
// under engine/agents/ and drives greeter_caller's agent_call chain to
// completion, proving the on-disk files aren't just documentation —
// they're valid, loadable, and actually runnable together.
func TestAgentsDir_GreeterCallerEndToEnd(t *testing.T) {
	registry, err := LoadAgentsFromDir("agents")
	if err != nil {
		t.Fatalf("LoadAgentsFromDir(\"agents\") failed: %v", err)
	}

	callerDef, ok := registry.Get("greeter_caller")
	if !ok {
		t.Fatal("expected greeter_caller to be loaded from engine/agents/")
	}
	if _, ok := registry.Get("greeter"); !ok {
		t.Fatal("expected greeter to be loaded from engine/agents/")
	}

	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry, discardLogger)

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, callerDef, "world")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	enqueuedRef, ok := run.EnqueuedSteps["call_greeter"]
	if !ok || len(enqueuedRef) < 4 || enqueuedRef[:4] != "run:" {
		t.Fatalf("expected call_greeter to spawn a child run, got %q", enqueuedRef)
	}
	childRunID := enqueuedRef[4:]

	// Simulate the worker completing the child run's only step (a
	// tool_call, resolved synchronously in real usage via the queue,
	// but driven directly here since this test doesn't run a worker).
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: childRunID, StepID: "greet", Output: "Hello, world!"}); err != nil {
		t.Fatalf("OnStepCompleted(greet) failed: %v", err)
	}

	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.StepResults["call_greeter"] != "Hello, world!" {
		t.Errorf("expected call_greeter result 'Hello, world!', got %q", run.StepResults["call_greeter"])
	}
	if _, enqueued := run.EnqueuedSteps["after_greeting"]; !enqueued {
		t.Errorf("expected after_greeting to be enqueued once the child run completed, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
}

// TestAgentsDir_MultiSourceResearchFanIn loads the real
// multi_source_research_agent.json and proves its fan-in dependency
// (answer depends_on both background_search and recent_search) is
// respected: answer must not enqueue until BOTH sources have
// completed, not just the first one to finish.
func TestAgentsDir_MultiSourceResearchFanIn(t *testing.T) {
	registry, err := LoadAgentsFromDir("agents")
	if err != nil {
		t.Fatalf("LoadAgentsFromDir(\"agents\") failed: %v", err)
	}

	def, ok := registry.Get("multi_source_research_agent")
	if !ok {
		t.Fatal("expected multi_source_research_agent to be loaded from engine/agents/")
	}

	server := newFakeQueueServer(t)
	defer server.Close()

	store := newFakeRunStore()
	orchestrator := NewOrchestrator(store, NewQueueClient(server.URL), registry, discardLogger)

	ctx := context.Background()
	run, err := orchestrator.CreateRun(ctx, def, "what is the James Webb Space Telescope")
	if err != nil {
		t.Fatalf("CreateRun failed: %v", err)
	}

	if _, enqueued := run.EnqueuedSteps["background_search"]; !enqueued {
		t.Fatalf("expected background_search to be enqueued after CreateRun, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
	if _, enqueued := run.EnqueuedSteps["recent_search"]; !enqueued {
		t.Fatalf("expected recent_search to be enqueued after CreateRun, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Complete only the first source. answer must NOT enqueue yet -
	// this is the actual fan-in behavior under test.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "background_search", Output: "general background info"}); err != nil {
		t.Fatalf("OnStepCompleted(background_search) failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if _, enqueued := run.EnqueuedSteps["answer"]; enqueued {
		t.Fatalf("expected answer NOT to be enqueued with only one of two sources completed, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}

	// Complete the second source. Now answer should enqueue.
	if err := orchestrator.OnStepCompleted(ctx, WebhookPayload{RunID: run.ID, StepID: "recent_search", Output: "recent developments info"}); err != nil {
		t.Fatalf("OnStepCompleted(recent_search) failed: %v", err)
	}
	run, err = orchestrator.GetRun(ctx, run.ID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if _, enqueued := run.EnqueuedSteps["answer"]; !enqueued {
		t.Fatalf("expected answer to be enqueued once both sources completed, got EnqueuedSteps=%v", run.EnqueuedSteps)
	}
}
