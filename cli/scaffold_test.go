package cli

import (
	engine "axon-engine"
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

var discardLogger = slog.New(slog.DiscardHandler)

// fakeRunStore/newFakeQueueServer/validates mirror the identical
// pattern in engine/agentbuilder/builder_test.go - needed here too so
// TestScaffold_AllTypesProduceValidAgents can exercise the engine's
// real (unexported) validateAgentDefinition via its one public entry
// point, CreateRun, rather than reimplementing validation rules here.
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

func validates(t *testing.T, def engine.AgentDefinition) error {
	t.Helper()
	server := newFakeQueueServer(t)
	orchestrator := engine.NewOrchestrator(newFakeRunStore(), engine.NewQueueClient(server.URL), engine.MapAgentRegistry{}, discardLogger)
	_, err := orchestrator.CreateRun(context.Background(), def, "test input")
	return err
}

func scaffoldAgent(t *testing.T, scaffoldType, name string) engine.AgentDefinition {
	t.Helper()
	data, err := Scaffold(scaffoldType, name)
	if err != nil {
		t.Fatalf("Scaffold(%q, %q) failed: %v", scaffoldType, name, err)
	}
	var def engine.AgentDefinition
	if err := json.Unmarshal(data, &def); err != nil {
		t.Fatalf("Scaffold(%q, %q) produced invalid JSON: %v", scaffoldType, name, err)
	}
	if def.Name != name {
		t.Errorf("expected Name %q, got %q", name, def.Name)
	}
	return def
}

func TestScaffold_ToolCall(t *testing.T) {
	def := scaffoldAgent(t, "tool_call", "my_agent")
	if len(def.Steps) != 1 {
		t.Fatalf("expected 1 step, got %d", len(def.Steps))
	}
	if def.Steps[0].Type != engine.StepTypeToolCall {
		t.Errorf("expected a tool_call step, got %q", def.Steps[0].Type)
	}
}

func TestScaffold_Conditional(t *testing.T) {
	def := scaffoldAgent(t, "conditional", "my_agent")

	var hasConditional bool
	for _, s := range def.Steps {
		if s.Type == engine.StepTypeConditional {
			hasConditional = true
			if s.OnTrue == "" || s.OnFalse == "" {
				t.Error("expected the conditional step to have both on_true and on_false set")
			}
		}
	}
	if !hasConditional {
		t.Error("expected a conditional step")
	}
	// Deliberately no output_step, see scaffoldConditional's comment:
	// only one branch actually runs per run.
	if def.OutputStep != "" {
		t.Errorf("expected no output_step (ambiguous under branching), got %q", def.OutputStep)
	}
}

func TestScaffold_Supervisor(t *testing.T) {
	def := scaffoldAgent(t, "supervisor", "my_agent")

	var supervisorStep *engine.StepDefinition
	for i, s := range def.Steps {
		if s.Type == engine.StepTypeSupervisor {
			supervisorStep = &def.Steps[i]
		}
	}
	if supervisorStep == nil {
		t.Fatal("expected a supervisor step")
	}
	if len(supervisorStep.Options) == 0 {
		t.Error("expected the supervisor to have at least one option")
	}
	if supervisorStep.FirstIterationStep == "" {
		t.Error("expected first_iteration_step to be set")
	}
}

func TestScaffold_AgentCall(t *testing.T) {
	def := scaffoldAgent(t, "agent_call", "my_agent")

	var hasAgentCall bool
	for _, s := range def.Steps {
		if s.Type == engine.StepTypeAgentCall {
			hasAgentCall = true
			if s.Agent == "" {
				t.Error("expected the agent_call step to name an agent, even a placeholder")
			}
		}
	}
	if !hasAgentCall {
		t.Error("expected an agent_call step")
	}
}

func TestScaffold_FanIn(t *testing.T) {
	def := scaffoldAgent(t, "fan_in", "my_agent")

	byID := make(map[string]engine.StepDefinition, len(def.Steps))
	for _, s := range def.Steps {
		byID[s.ID] = s
	}

	combine, ok := byID["combine"]
	if !ok {
		t.Fatal("expected a combine step")
	}
	if len(combine.DependsOn) != 2 {
		t.Errorf("expected combine to depend on exactly 2 branches, got %v", combine.DependsOn)
	}

	for _, depID := range combine.DependsOn {
		branch, ok := byID[depID]
		if !ok {
			continue
		}
		if len(branch.DependsOn) != 0 {
			t.Errorf("expected branch %q to have no dependencies (so it runs in parallel with the other branch), got %v", depID, branch.DependsOn)
		}
	}
}

func TestScaffold_UnknownType(t *testing.T) {
	if _, err := Scaffold("not_a_real_type", "my_agent"); err == nil {
		t.Fatal("expected an error for an unknown scaffold type, got none")
	}
}

// TestScaffold_AllTypesProduceValidAgents covers every entry in
// ScaffoldTypes against the real engine validation and execution path
// (via engine.NewOrchestrator(...).CreateRun, the only way to exercise
// validateAgentDefinition from outside the engine package - see
// engine/agentbuilder/builder_test.go's identical pattern), so a new
// scaffold type added to ScaffoldTypes without a matching case in
// Scaffold, or one that doesn't actually validate, fails loudly here
// instead of only being caught by someone running axon init for real.
//
// agent_call is the deliberate exception: CreateRun doesn't just
// validate, it also tries to actually run the first eligible step, and
// the scaffold's whole point is naming a sub-agent that doesn't exist
// yet (a real agent to call, same as tool_call's scaffold naming
// "echo" as a placeholder tool) - so "unknown agent" here is expected,
// not a bug, and is asserted on explicitly instead of being silently
// excluded from this check.
func TestScaffold_AllTypesProduceValidAgents(t *testing.T) {
	for _, scaffoldType := range ScaffoldTypes {
		t.Run(scaffoldType, func(t *testing.T) {
			def := scaffoldAgent(t, scaffoldType, "test_"+scaffoldType)
			err := validates(t, def)
			if scaffoldType == "agent_call" {
				if err == nil || !strings.Contains(err.Error(), "unknown agent") {
					t.Errorf("expected agent_call's scaffold to fail with \"unknown agent\" (its placeholder sub-agent isn't registered), got: %v", err)
				}
				return
			}
			if err != nil {
				t.Errorf("scaffold %q failed validation: %v", scaffoldType, err)
			}
		})
	}
}
