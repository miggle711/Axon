// engine/api/types.go
package api

import (
	engine "axon-engine"
)

// CreateRunRequest is what the client sends to POST /runs. Exactly one
// of AgentName or Definition must be set: AgentName resolves via the
// server's AgentRegistry (see #40 - lets a client like the CLI start a
// run without needing its own access to agent definitions), Definition
// is used inline as before.
type CreateRunRequest struct {
	AgentName  string                  `json:"agent_name,omitempty"`
	Definition *engine.AgentDefinition `json:"definition,omitempty"`
	Input      string                  `json:"input" binding:"required"`
}

type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

type SuccessResponse struct {
	Message string `json:"message"`
}
