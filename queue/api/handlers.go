package api

import (
	"context"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"
	jobpkg "axon-queue"
)

// healthHandler - GET /health
// Returns: {"status": "ok"}
func (s *Server) healthHandler(c *gin.Context) {
	c.JSON(200, HealthResponse{Status: "ok"})
}

// statsHandler - GET /stats
// Returns: Queue statistics
func (s *Server) statsHandler(c *gin.Context) {
	stats, err := s.queue.GetStats(context.Background())
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to get stats", Code: 500})
		return
	}
	if stats == nil {
		c.JSON(500, ErrorResponse{Error: "Failed to get stats", Code: 500})
		return
	}
	c.JSON(200, StatsResponse{
		PendingJobs:    stats.PendingJobs,
		InProgressJobs: stats.InProgressJobs,
		CompletedJobs:  stats.CompletedJobs,
		FailedJobs:     stats.FailedJobs,
		Throughput:     stats.Throughput,
	})
}

// enqueueHandler - POST /jobs
// Input: {"type": "...", "payload": "...", "priority": ...}
// Returns: 201 + job ID
func (s *Server) enqueueHandler(c *gin.Context) {
	var req EnqueueRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, ErrorResponse{Error: "Invalid request body", Code: 400})
		return
	}

	job := &jobpkg.Job{
		ID:         uuid.New().String(),
		Type:       req.Type,
		Payload:    req.Payload,
		Priority:   req.Priority,
		Status:     "pending",
		Retries:    0,
		MaxRetries: 3,
		CreatedAt:  time.Now().Unix(),
		RunAt:      time.Now().Unix(),
	}

	err := s.queue.Enqueue(c.Request.Context(), job)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to enqueue job", Code: 500})
		return
	}

	c.JSON(201, JobResponse{
		ID:         job.ID,
		Type:       job.Type,
		Payload:    job.Payload,
		Priority:   job.Priority,
		Status:     job.Status,
		Retries:    job.Retries,
		MaxRetries: job.MaxRetries,
		CreatedAt:  job.CreatedAt,
		RunAt:      job.RunAt,
	})
}

// dequeueHandler - GET /jobs/next
// Returns: Next job to process OR 204 if empty
func (s *Server) dequeueHandler(c *gin.Context) {
	job, err := s.queue.Dequeue(c.Request.Context())
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to dequeue job", Code: 500})
		return
	}
	if job == nil {
		c.Status(204)
		return
	}

	c.JSON(200, JobResponse{
		ID:         job.ID,
		Type:       job.Type,
		Payload:    job.Payload,
		Priority:   job.Priority,
		Status:     job.Status,
		Retries:    job.Retries,
		MaxRetries: job.MaxRetries,
		CreatedAt:  job.CreatedAt,
		RunAt:      job.RunAt,
	})
}

// ackHandler - POST /jobs/:id/ack
// Marks a job as successfully completed
func (s *Server) ackHandler(c *gin.Context) {
	jobID := c.Param("id")
	err := s.queue.Ack(c.Request.Context(), jobID)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to acknowledge job", Code: 500})
		return
	}

	c.JSON(200, gin.H{"message": "Job acknowledged successfully"})
}

// failHandler - POST /jobs/:id/fail
// Marks a job as failed
// Input: {"reason": "..."}
func (s *Server) failHandler(c *gin.Context) {
	jobID := c.Param("id")

	var req FailRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(400, ErrorResponse{Error: "Invalid request body", Code: 400})
		return
	}

	err := s.queue.Nack(c.Request.Context(), jobID, req.Reason)
	if err != nil {
		c.JSON(500, ErrorResponse{Error: "Failed to mark job as failed", Code: 500})
		return
	}

	c.JSON(200, gin.H{"message": "Job marked as failed successfully"})
}
