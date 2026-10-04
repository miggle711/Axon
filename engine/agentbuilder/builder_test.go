package agentbuilder

import (
	engine "axon-engine"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"testing"
)

func jsonFile(path string) ([]byte, error) {
	return os.ReadFile(path)
}

var discardLogger = slog.New(slog.DiscardHandler)

// fakeRunStore/newFakeQueueServer mirror the test doubles in
// engine/orchestrator_test.go, needed here only to exercise
// engine.NewOrchestrator(...).CreateRun, the public entry point that
// runs the engine's own (unexported) validateAgentDefinition - the
// real source of truth, not reimplemented or duplicated here.
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

func (s *fakeRunStore) ListRuns(ctx context.Context, opts engine.ListRunsOptions) ([]*engine.Run, error) {
	return nil, nil
}

func newFakeQueueServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`{"id":"job-1"}`))
	}))
	t.Cleanup(server.Close)
	return server
}

// validates runs def through the engine's real CreateRun, the only way
// to exercise validateAgentDefinition (unexported) from outside the
// engine package.
func validates(t *testing.T, def engine.AgentDefinition) error {
	t.Helper()
	server := newFakeQueueServer(t)
	orchestrator := engine.NewOrchestrator(newFakeRunStore(), engine.NewQueueClient(server.URL), engine.MapAgentRegistry{}, discardLogger)
	_, err := orchestrator.CreateRun(context.Background(), def, "test input")
	return err
}

// unmarshalAgent round-trips data through engine.AgentDefinition, the
// same type engine.LoadAgentsFromDir unmarshals into, so comparisons
// below are structural (field values), not textual - whitespace or key
// order differences between a builder's output and a hand-written file
// don't matter, only the actual AgentDefinition they produce.
func unmarshalAgent(t *testing.T, data []byte) engine.AgentDefinition {
	t.Helper()
	var def engine.AgentDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		t.Fatalf("failed to unmarshal: %v", err)
	}
	return def
}

// TestBuilder_MatchesHandWrittenMultiSourceResearchAgent rebuilds the
// real, committed multi_source_research_agent.json via the builder and
// proves it produces the exact same AgentDefinition - the strongest
// evidence the builder's output is interchangeable with what's already
// hand-authored in this repo, not just internally self-consistent.
func TestBuilder_MatchesHandWrittenMultiSourceResearchAgent(t *testing.T) {
	backgroundSearch := Tool("background_search", "tavily_search", "{{user_input}}")
	recentSearch := Tool("recent_search", "tavily_search", "{{user_input}} latest news recent developments")
	answer := LLM("answer",
		"Answer the question below by combining both sets of search results. Note where the general background and the recent developments agree, and call out anything in the recent results that updates or adds to the background. Be concise.\n\nQuestion: {{user_input}}\n\nGeneral background:\n"+Output(backgroundSearch)+"\n\nRecent developments:\n"+Output(recentSearch),
		backgroundSearch, recentSearch,
	)

	built := New("multi_source_research_agent", backgroundSearch, recentSearch, answer).
		WithDescription("Runs two independent Tavily searches on the same question from different angles (general background, and recent developments), then synthesizes a single answer from both. Requires TAVILY_API_KEY and GROQ_API_KEY to be set on the worker. Exercises a genuine fan-in dependency (one step depending on two others) that no other agent in this repo has needed yet.").
		WithOutputStep(answer).
		Definition()

	committedJSON, err := jsonFile("../agents/multi_source_research_agent.json")
	if err != nil {
		t.Fatalf("failed to read the real committed agent: %v", err)
	}
	committed := unmarshalAgent(t, committedJSON)

	builtJSON, err := New("multi_source_research_agent", backgroundSearch, recentSearch, answer).
		WithDescription(built.Description).
		WithOutputStep(answer).
		JSON()
	if err != nil {
		t.Fatalf("JSON() failed: %v", err)
	}
	fromBuilder := unmarshalAgent(t, builtJSON)

	if !reflect.DeepEqual(committed, fromBuilder) {
		t.Errorf("builder output does not match the hand-written agent.\ncommitted: %+v\nfrom builder: %+v", committed, fromBuilder)
	}
}

func TestBuilder_Conditional_WiresReverseDependency(t *testing.T) {
	step1 := Tool("step_1", "echo", "success")
	onTrue := Tool("on_true_step", "echo", "took the true branch")
	onFalse := Tool("on_false_step", "echo", "took the false branch")
	check := Conditional("check", Output(step1)+" == success", onTrue, onFalse, step1)

	agent := New("conditional_test_agent", step1, check, onTrue, onFalse).Definition()

	if err := validates(t, agent); err != nil {
		t.Fatalf("expected the built agent to validate cleanly, got: %v", err)
	}

	byID := make(map[string]engine.StepDefinition, len(agent.Steps))
	for _, s := range agent.Steps {
		byID[s.ID] = s
	}

	if !contains(byID["on_true_step"].DependsOn, "check") {
		t.Errorf("expected on_true_step to depend on check, got %v", byID["on_true_step"].DependsOn)
	}
	if !contains(byID["on_false_step"].DependsOn, "check") {
		t.Errorf("expected on_false_step to depend on check, got %v", byID["on_false_step"].DependsOn)
	}
	if byID["check"].OnTrue != "on_true_step" {
		t.Errorf("expected check.OnTrue to be on_true_step, got %q", byID["check"].OnTrue)
	}
	if byID["check"].OnFalse != "on_false_step" {
		t.Errorf("expected check.OnFalse to be on_false_step, got %q", byID["check"].OnFalse)
	}
}

func TestBuilder_Supervisor_WiresReverseDependencyAndFirstIteration(t *testing.T) {
	optionA := Tool("option_a", "echo", "ran option a")
	supervisor := Supervisor("supervisor_step", "decide", []*Step{optionA}).FirstIteration(optionA)

	agent := New("supervisor_test_agent", supervisor, optionA).Definition()

	if err := validates(t, agent); err != nil {
		t.Fatalf("expected the built agent to validate cleanly, got: %v", err)
	}

	byID := make(map[string]engine.StepDefinition, len(agent.Steps))
	for _, s := range agent.Steps {
		byID[s.ID] = s
	}

	if !contains(byID["option_a"].DependsOn, "supervisor_step") {
		t.Errorf("expected option_a to depend on supervisor_step, got %v", byID["option_a"].DependsOn)
	}
	if byID["supervisor_step"].FirstIterationStep != "option_a" {
		t.Errorf("expected first_iteration_step to be option_a, got %q", byID["supervisor_step"].FirstIterationStep)
	}
}

func contains(ss []string, target string) bool {
	for _, s := range ss {
		if s == target {
			return true
		}
	}
	return false
}
