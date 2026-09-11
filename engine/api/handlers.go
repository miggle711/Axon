package api

import (
	engine "axon-engine"
	"errors"
	"strconv"

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

func (s *Server) webhookFailedHandler(c *gin.Context) {
	var payload engine.WebhookFailedPayload
	if err := c.ShouldBindJSON(&payload); err != nil {
		c.JSON(400, ErrorResponse{Error: "Invalid request body", Code: 400})
		return
	}

	if payload.RunID == "" || payload.StepID == "" || payload.Reason == "" {
		c.JSON(400, ErrorResponse{Error: "Missing required fields", Code: 400})
		return
	}

	err := s.orchestrator.OnStepFailed(c.Request.Context(), payload)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to mark step as failed", Code: 500})
		return
	}

	c.JSON(200, SuccessResponse{Message: "Step marked as failed"})
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

// listRunsHandler lists top-level runs, newest first (#53). Query
// params: agent_name and status filter (both optional, ANDed
// together), limit and offset paginate (limit defaults to 20, capped
// at 100 - see engine.ListRunsOptions). An unparseable limit/offset is
// treated as unset rather than rejected, since defaulting silently is
// friendlier for a list endpoint someone's likely to hit by hand.
func (s *Server) listRunsHandler(c *gin.Context) {
	opts := engine.ListRunsOptions{
		AgentName: c.Query("agent_name"),
		Status:    c.Query("status"),
	}
	if limit, err := strconv.Atoi(c.Query("limit")); err == nil {
		opts.Limit = limit
	}
	if offset, err := strconv.Atoi(c.Query("offset")); err == nil {
		opts.Offset = offset
	}

	runs, err := s.orchestrator.ListRuns(c.Request.Context(), opts)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to list runs", Code: 500})
		return
	}

	c.JSON(200, runs)
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
		// A malformed agent definition is the caller's mistake, not an
		// internal failure - surface validateAgentDefinition's actual
		// message (which now collects every problem found, not just
		// the first, see #54) instead of a generic 500 that hides it.
		if errors.Is(err, engine.ErrInvalidAgentDefinition) {
			c.JSON(400, ErrorResponse{Error: err.Error(), Code: 400})
			return
		}
		c.JSON(500, ErrorResponse{Error: "Failed to create run", Code: 500})
		return
	}

	c.JSON(201, run)
}
