package worker

// Job type strings, mirroring engine.StepType. Kept as local
// constants rather than importing axon-engine, matching how the
// other types in this file avoid a cross-module dependency.
const (
	JobTypeToolCall   = "tool_call"
	JobTypeLLMCall    = "llm_call"
	JobTypeSupervisor = "supervisor"
)

// SupervisorDoneSignal mirrors engine.SupervisorDoneSignal. Kept as a
// local constant rather than importing axon-engine, for the same
// reason as the job type constants above - this value is part of the
// wire contract (it flows through StepPayload/WebhookPayload as plain
// text), not an internal engine detail the worker needs the package for.
const SupervisorDoneSignal = "done"

type JobResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type StepPayload struct {
	RunID     string   `json:"run_id"`
	StepID    string   `json:"step_id"`
	AgentName string   `json:"agent_name"`
	Tool      string   `json:"tool,omitempty"`    // which tool to dispatch to, for tool_call steps
	Options   []string `json:"options,omitempty"` // the valid choices (excluding "done"), for supervisor steps
	Input     string   `json:"input"`
}

type WebhookPayload struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
	Output string `json:"output"`
}
