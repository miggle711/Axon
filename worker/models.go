package worker

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
