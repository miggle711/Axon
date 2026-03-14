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
	store *store.RedisStore
}

type Stats struct {
	PendingJobs    int `json:"pending_jobs"`     // number of jobs currently waiting in the queue to be processed
	InProgressJobs int `json:"in_progress_jobs"` // number of jobs currently being processed by workers
	CompletedJobs  int `json:"completed_jobs"`   // number of jobs that have been successfully completed
	FailedJobs     int `json:"failed_jobs"`      // number of jobs that have failed after reaching max retries
	Throughput     int `json:"throughput"`       // number of jobs processed per unit time
}

func NewQueue(store *store.RedisStore) *Queue {
	// Queue constructor that initializes the queue with a Redis store for job management and persistence.
	// Abstracts away the underlying storage mechanism, allowing for potential future support of other storage backends without changing the queue interface.
	return &Queue{store: store}
}

func (q *Queue) Enqueue(ctx context.Context, job *queue.Job) error {
	err := q.store.StoreJob(job)
	if err != nil {
		return err
	}
	return q.store.AddToPending(job, job.Priority)
}

func (q *Queue) Dequeue(ctx context.Context) (*queue.Job, error) {
	// Implementation to retrieve and remove a job from the queue using the Redis store.
	return q.store.PopFromPending()
}

func (q *Queue) Ack(ctx context.Context, jobID string) error {
	// Implementation to acknowledge the completion of a job, which may involve removing it from a processing list or marking it as completed in the Redis store.
	return nil
}

func (q *Queue) Nack(ctx context.Context, jobID string, reason string) error {
	// Implementation to negatively acknowledge a job, which may involve re-queuing it or marking it as failed in the Redis store.
	return nil
}

func (q *Queue) GetJobStatus(ctx context.Context, jobID string) (string, error) {
	// Implementation to retrieve the current status of a job from the Redis store.
	return "", nil
}

func (q *Queue) GetStats(ctx context.Context) (*Stats, error) {
	// Implementation to gather and return statistics about the queue, such as the number of pending, in-progress, completed, and failed jobs, as well as throughput.
	return nil, nil
}

// Redis sorted set: for job scheduling and prioritization, where the score can represent the scheduled run time or priority level.
//     - jobs sorted by priority
// Redis hash: store job details with job ID as the key and job data as the value, allowing for quick retrieval and updates of job information.
// set to track in progress jobs, allowing for quick membership checks and management of currently processing jobs.
// list to manage dead letter queue for failed jobs, allowing for easy retrieval and analysis of jobs that have exceeded their retry limits.
