// engine/api/types.go
package api

import (
	engine "axon-engine"
)

// CreateRunRequest is what the client sends to POST /runs
type CreateRunRequest struct {
	Definition engine.AgentDefinition `json:"definition" binding:"required"`
	Input      string                 `json:"input" binding:"required"`
}

type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}

type SuccessResponse struct {
	Message string `json:"message"`
}
