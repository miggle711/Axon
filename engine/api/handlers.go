package api

import (
	"github.com/gin-gonic/gin"
	engine "axon-engine"
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
	err := s.orchestrator.OnStepCompleted(c.Request.Context(),payload)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to mark step as complete", Code: 500})
		return
	}

	c.JSON(200, SuccessResponse{Message: "Step marked as complete"})
}




	
