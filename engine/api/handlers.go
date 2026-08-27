package api

import (
	engine "axon-engine"
	"github.com/gin-gonic/gin"
)

func (s *Server) webhookCompleteHandler(c *gin.Context) {
	// parse the request body to get the run ID and status
	var payload engine.WebhookPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, ErrorResponse{Error: "Invalid request body", Code: 400})
		return
	}

	// validate the payload (run id step id and output are required)
	if payload.RunID == "" || payload.StepID == "" || payload.Output == "" {
		c.JSON(400, ErrorResponse{Error: "Missing required fields", Code: 400})
		return
	}

	// call the orchestrator to mark the step as complete
	err := s.orchestrator.OnStepCompleted(c.Request.Context(), payload)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to mark step as complete", Code: 500})
		return
	}

	c.JSON(200, SuccessResponse{Message: "Step marked as complete"})
}

func (s *Server) getRunHandler(c *gin.Context) {
	runID := c.Param("id")
	if runID == "" {
		c.JSON(400, ErrorResponse{Error: "Missing run ID", Code: 400})
		return
	}

	run, err := s.orchestrator.GetRun(c.Request.Context(), runID)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to retrieve run", Code: 500})
		return
	}

	c.JSON(200, run)
}

func (s *Server) createRunHandler(c *gin.Context) {
	var request CreateRunRequest
	if err := c.ShouldBindJSON(&request); err != nil {
		c.JSON(400, ErrorResponse{Error: "Invalid request body", Code: 400})
		return
	}

	hasAgentName := request.AgentName != ""
	hasDefinition := request.Definition != nil
	if hasAgentName == hasDefinition { // both set, or neither set
		c.JSON(400, ErrorResponse{Error: "Exactly one of agent_name or definition is required", Code: 400})
		return
	}

	var run *engine.Run
	var err error
	if hasAgentName {
		run, err = s.orchestrator.CreateRunByName(c.Request.Context(), request.AgentName, request.Input)
	} else {
		run, err = s.orchestrator.CreateRun(c.Request.Context(), *request.Definition, request.Input)
	}
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to create run", Code: 500})
		return
	}

	c.JSON(201, run)
}
