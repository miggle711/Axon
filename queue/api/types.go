package api

// EnqueueRequest is what the client sends to POST /jobs
type EnqueueRequest struct {
	Type     string `json:"type" binding:"required"`      // "search", "email", etc.
	Payload  string `json:"payload" binding:"required"`   // JSON string with job data
	Priority int    `json:"priority" binding:"required"`  // Higher = more important
}

// JobResponse is returned when a job is enqueued or dequeued
type JobResponse struct {
	ID        string `json:"id"`
	Type      string `json:"type"`
	Payload   string `json:"payload"`
	Priority  int    `json:"priority"`
	Status    string `json:"status"`
	Retries   int    `json:"retries"`
	MaxRetries int   `json:"max_retries"`
	CreatedAt int64  `json:"created_at"`
	RunAt     int64  `json:"run_at"`
}

// FailRequest is what the client sends to POST /jobs/:id/fail
type FailRequest struct {
	Reason string `json:"reason" binding:"required"` // Why the job failed
}

// StatsResponse is returned by GET /stats
type StatsResponse struct {
	PendingJobs    int64   `json:"pending_jobs"`
	InProgressJobs int64   `json:"in_progress_jobs"`
	CompletedJobs  int64   `json:"completed_jobs"`
	FailedJobs     int64   `json:"failed_jobs"`
	Throughput     float64 `json:"throughput"` // jobs per second
}

// HealthResponse is returned by GET /health
type HealthResponse struct {
	Status string `json:"status"`
}

// ErrorResponse is returned on errors
type ErrorResponse struct {
	Error string `json:"error"`
	Code  int    `json:"code"`
}
