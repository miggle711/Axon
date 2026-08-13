package worker

// Job type strings, mirroring engine.StepType. Kept as local
// constants rather than importing axon-engine, matching how the
// other types in this file avoid a cross-module dependency.
const (
	JobTypeToolCall = "tool_call"
	JobTypeLLMCall  = "llm_call"
)

type JobResponse struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Payload string `json:"payload"`
}

type StepPayload struct {
	RunID     string `json:"run_id"`
	StepID    string `json:"step_id"`
	AgentName string `json:"agent_name"`
	Input     string `json:"input"`
}

type WebhookPayload struct {
	RunID  string `json:"run_id"`
	StepID string `json:"step_id"`
	Output string `json:"output"`
}
