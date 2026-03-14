package queue

import (
	queue "axon-queue"
	"axon-queue/internal/store"
	"context"
)

//   Job lifecycle:                                                                                                                                                                                          
//   1. Enqueue > Store job + add to pending queue
//   2. Dequeue > Remove from pending + add to running                                                                                                                                                       
//   3. Ack > Remove from running + update job status to "completed"
//   4. Nack > Remove from running + add to failed list + update job status to "failed"

type Queue struct {
	jobStore store.JobStore
	ops      store.QueueOperations
	metrics  store.QueueMetrics
}

type Stats struct {
	PendingJobs    int64   `json:"pending_jobs"`     // number of jobs currently waiting in the queue to be processed
	InProgressJobs int64   `json:"in_progress_jobs"` // number of jobs currently being processed by workers
	CompletedJobs  int64   `json:"completed_jobs"`   // number of jobs that have been successfully completed
	FailedJobs     int64   `json:"failed_jobs"`      // number of jobs that have failed after reaching max retries
	Throughput     float64 `json:"throughput"`       // number of jobs processed per unit time
}

// NewQueue creates a Queue with separate interfaces for job storage, queue operations, and metrics.
// Allows for flexible implementations of each concern.
func NewQueue(jobStore store.JobStore, ops store.QueueOperations, metrics store.QueueMetrics) *Queue {
	return &Queue{
		jobStore: jobStore,
		ops:      ops,
		metrics:  metrics,
	}
}

func (q *Queue) Enqueue(ctx context.Context, job *queue.Job) error {
	err := q.jobStore.StoreJob(ctx, job)
	if err != nil {
		return err
	}
	return q.ops.AddToPending(ctx, job, job.Priority)
}

func (q *Queue) Dequeue(ctx context.Context) (*queue.Job, error) {
	// retrieve and remove a job from the queue using the Redis store.
	job, err := q.ops.PopFromPending(ctx)
	if err != nil {
		return nil, err
	}
	if job != nil {
		err = q.ops.AddToRunning(ctx, job)
		if err != nil {
			return nil, err
		}
	}
	return job, nil
}

func (q *Queue) Ack(ctx context.Context, jobID string) error {
	// Job completion handler
	// Remove the job from the running_jobs set
	err := q.ops.RemoveFromRunning(ctx, jobID)
	if err != nil {
		return err
	}
	// Track as completed for throughput calculation
	err = q.ops.AddToCompleted(ctx, jobID)
	if err != nil {
		return err
	}
	return q.jobStore.UpdateJobStatus(ctx, jobID, "completed")
}

func (q *Queue) Nack(ctx context.Context, jobID string, reason string) error {
	// Job failure handler
	// Remove the job from the running_jobs set and add it to the failed_jobs list
	err := q.ops.RemoveFromRunning(ctx, jobID)
	if err != nil {
		return err
	}
	// Retrieve the job to pass to AddToFailed
	job, err := q.jobStore.GetJob(ctx, jobID)
	if err != nil {
		return err
	}
	if job != nil {
		err = q.ops.AddToFailed(ctx, job, reason)
		if err != nil {
			return err
		}
	}
	return q.jobStore.UpdateJobStatus(ctx, jobID, "failed")
}

func (q *Queue) GetJobStatus(ctx context.Context, jobID string) (string, error) {
	// retrieve the current status of a job.
	job, err := q.jobStore.GetJob(ctx, jobID)
	if err != nil {
		return "", err
	}
	if job == nil {
		return "not found", nil // job does not exist in the store
	}
	return job.Status, nil
}

func (q *Queue) GetStats(ctx context.Context) (*Stats, error) {
	// Gather and return statistics about the queue.
	pendingCount, err := q.metrics.GetPendingCount(ctx)
	if err != nil {
		return nil, err
	}
	inProgressCount, err := q.metrics.GetInProgressCount(ctx)
	if err != nil {
		return nil, err
	}
	completedCount, err := q.metrics.GetCompletedCount(ctx)
	if err != nil {
		return nil, err
	}
	failedCount, err := q.metrics.GetFailedCount(ctx)
	if err != nil {
		return nil, err
	}
	throughput, err := q.metrics.GetThroughput(ctx)
	if err != nil {
		return nil, err
	}
	return &Stats{
		PendingJobs:    pendingCount,
		InProgressJobs: inProgressCount,
		CompletedJobs:  completedCount,
		FailedJobs:     failedCount,
		Throughput:     throughput,
	}, nil
}

// Redis sorted set: for job scheduling and prioritization, where the score can represent the scheduled run time or priority level.
//     - jobs sorted by priority
// Redis hash: store job details with job ID as the key and job data as the value, allowing for quick retrieval and updates of job information.
// set to track in progress jobs, allowing for quick membership checks and management of currently processing jobs.
// list to manage dead letter queue for failed jobs, allowing for easy retrieval and analysis of jobs that have exceeded their retry limits.
