package engine

import "time"

// StepType represents the type of a step in the workflow
// Better to use a named type for clarity instead of magic strings
type StepType string

const (
	StepTypeToolCall    StepType = "tool_call"
	StepTypeLLMCall     StepType = "llm_call"
	StepTypeConditional StepType = "conditional"
	StepTypeAgentCall   StepType = "agent_call"
	StepTypeSupervisor  StepType = "supervisor"
)

// SupervisorDoneSignal is the reserved value a supervisor step's
// PromptTemplate must instruct its LLM to answer with in order to stop
// looping, as opposed to answering with one of the step's Options.
const SupervisorDoneSignal = "done"

// MaxSupervisorIterations caps how many Options a single supervisor
// step may choose across its loop before being force-stopped.
const MaxSupervisorIterations = 5

// a single step in the workflow
type StepDefinition struct {
	// Core fields
	ID        string   `json:"id"`
	Type      StepType `json:"type"`       // tool_call, llm_call, conditional, agent_call, supervisor
	DependsOn []string `json:"depends_on"` // list of step IDs this step depends on

	// tool calls and llm calls
	Tool           string `json:"tool,omitempty"`           // for tool calls
	InputTemplate  string `json:"input_template,omitempty"` // for tool input
	Agent          string `json:"agent,omitempty"`          // for agent calls
	PromptTemplate string `json:"prompt_template,omitempty"`

	// Conditional routing
	Condition string `json:"condition,omitempty"` // condition to evaluate
	OnTrue    string `json:"on_true,omitempty"`   // step ID if condition is true
	OnFalse   string `json:"on_false,omitempty"`  // step ID if condition is false

	// Supervisor routing
	Options []string `json:"options,omitempty"` // step IDs the supervisor can choose

}

// the agent blueprint
type AgentDefinition struct {
	Name        string           `json:"name"`
	Description string           `json:"description"`
	Steps       []StepDefinition `json:"steps"`

	// OutputStep names the step whose result becomes this agent's
	// output when it's spawned as a child run via an agent_call step.
	// Only required for agents used as sub-agents.
	OutputStep string `json:"output_step,omitempty"`
}

// the execution state of a workflow run (session)
type Run struct {
	ID        string `json:"id"`
	AgentName string `json:"agent_name"`
	UserInput string `json:"user_input"`
	Status    string `json:"status"` // pending, in_progress, completed, failed

	Steps []StepDefinition `json:"steps"` // snapshot of the agent's steps at run creation

	StepResults   map[string]string `json:"step_results"`   // step_id > output
	EnqueuedSteps map[string]string `json:"enqueued_steps"` // step_id > job_id or "run:<child_run_id>"

	CompletedSteps []string `json:"completed_steps"`
	FailedSteps    []string `json:"failed_steps"`
	SkippedSteps   []string `json:"skipped_steps"`

	ParentRunID  string `json:"parent_run_id"`  // empty if this is a top-level run
	ParentStepID string `json:"parent_step_id"` // the agent_call step that created this child

	SupervisorIterations map[string]int `json:"supervisor_iterations"` // supervisor_id → iteration count (max 5)

	// ActiveSupervisorChoice tracks, for each currently-looping
	// supervisor step, which Options step it most recently chose and is
	// waiting on. Cleared for a supervisor once it stops (LLM says
	// "done", or the iteration cap is hit). Lets OnStepCompleted tell
	// an option step's completion apart from a normal step's: if the
	// completing step ID is some supervisor's active choice, control
	// returns to that supervisor for its next decision instead of
	// following normal DAG progression.
	ActiveSupervisorChoice map[string]string `json:"active_supervisor_choice"`

	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// StepPayload represents the data payload for a single step in the workflow
type StepPayload struct {
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	AgentName string `json:"agent_name"`
	Input     string `json:"input"`
}

// WebhookPayload (what workers POST back):
type WebhookPayload struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
	Output string `json:"output"`
}
