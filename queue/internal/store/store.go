package store


import (
	"context"
	"github.com/redis/go-redis/v9"
	"strconv"
	"time"
	"axon-queue"
	"fmt"
)

// JobStore interface handles job persistence
type JobStore interface {
	// standard interface for interacting with underlying storage
	// prevents tight coupling between the queue implementation and the specific storage mechanism
	StoreJob(ctx context.Context, job *queue.Job) error
	GetJob(ctx context.Context, jobID string) (*queue.Job, error)
	UpdateJobStatus(ctx context.Context, jobID string, status string) error
}

// QueueOperations interface handles all queue state transitions
type QueueOperations interface {
	AddToPending(ctx context.Context, job *queue.Job, priority int) error // adds a job to the pending queue with priority
	PopFromPending(ctx context.Context) (*queue.Job, error) // pops highest priority job from pending
	AddToRunning(ctx context.Context, job *queue.Job) error // moves job to running state
	RemoveFromRunning(ctx context.Context, jobID string) error // removes job from running state
	AddToFailed(ctx context.Context, job *queue.Job, reason string) error // moves job to failed state
	AddToCompleted(ctx context.Context, jobID string) error // tracks job as completed with timestamp
}

// QueueMetrics interface handles queue statistics and monitoring
type QueueMetrics interface {
	GetPendingCount(ctx context.Context) (int64, error)
	GetInProgressCount(ctx context.Context) (int64, error)
	GetCompletedCount(ctx context.Context) (int64, error)
	GetFailedCount(ctx context.Context) (int64, error)
	GetThroughput(ctx context.Context) (float64, error)
}
	

type RedisStore struct {
	// RedisStore is a concrete implementation of the Store interface that uses Redis as the underlying storage mechanism for managing job data in the queue system.
	client *redis.Client
}

func NewRedisStore(redisURL string) (*RedisStore, error) {
	// Initializes a new Redis client using the provided Redis URL and returns a RedisStore instance for managing job storage and retrieval.
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	return &RedisStore{client: client}, nil
}

func (s *RedisStore) AddToPending(ctx context.Context, job *queue.Job, priority int) error {
	// Implementation to add a job to the pending queue in Redis, using a sorted set to manage job prioritization based on the provided priority level.
	return s.client.ZAdd(ctx, "pending_jobs", redis.Z{
		Score:  float64(priority), // use the priority as the score for sorting in the Redis sorted set
		Member: job.ID, // use the job ID as the member of the sorted set for easy retrieval
	}).Err()
}

func (s *RedisStore) PopFromPending(ctx context.Context) (*queue.Job, error) {
	// Implementation to pop the highest priority job from the pending queue in Redis, allowing for efficient retrieval of jobs based on their scheduling and prioritization.
	id, err := s.client.ZPopMax(ctx, "pending_jobs").Result()
	if err != nil {
		return nil, err
	}
	if len(id) == 0 {
		return nil, nil // no pending jobs available
	}
	jobID := id[0].Member.(string)
	return s.GetJob(ctx, jobID) // retrieve the job details using the job ID
}

func (s *RedisStore) AddToRunning(ctx context.Context, job *queue.Job) error {
	// Implementation to add a job to the running set in Redis, allowing for tracking of jobs that are currently being processed by workers.
	return s.client.SAdd(ctx, "running_jobs", job.ID).Err()
}

func (s *RedisStore) RemoveFromRunning(ctx context.Context, jobID string) error {
	// Implementation to remove a job from the running set in Redis, allowing for tracking of jobs that have completed processing or have been acknowledged by workers.
	return s.client.SRem(ctx, "running_jobs", jobID).Err()
}

func (s *RedisStore) AddToFailed(ctx context.Context, job *queue.Job, reason string) error {
	// Implementation to add a job to the failed set in Redis, allowing for tracking of jobs that have failed after reaching the maximum number of retries.
	failedEntry := fmt.Sprintf("%s:%s", job.ID, reason)
	return s.client.LPush(ctx, "failed_jobs", failedEntry).Err()
}


func (s *RedisStore) StoreJob(ctx context.Context, job *queue.Job) error {
	// Implementation to store the job details in the Redis hash, allowing for quick retrieval and updates of job information.
	// convert the Job struct to a hash map to be stored as a Redis Hash
	jobMap := map[string]interface{}{
		"id":          job.ID,
		"type":        job.Type,
		"payload":     job.Payload,
		"status":      job.Status,
		"priority":    job.Priority,
		"retries":     job.Retries,
		"max_retries": job.MaxRetries,
		"created_at":  strconv.FormatInt(job.CreatedAt, 10),
		"run_at":      strconv.FormatInt(job.RunAt, 10),
	}

	err := s.hset(ctx, "job:"+job.ID, jobMap)
	if err != nil {
		return err
	}
	return nil
}

// hset is a private helper method for setting Redis hash values
func (s *RedisStore) hset(ctx context.Context, key string, values map[string]interface{}) error {
	return s.client.HSet(ctx, key, values).Err()
}

func (s *RedisStore) GetJob(ctx context.Context, jobID string) (*queue.Job, error) {
	// Implementation to retrieve job details from the Redis hash using the job ID as the key.
	jobMap, err := s.hgetall(ctx, "job:"+jobID)
	if err != nil {
		return nil, err
	}
	if len(jobMap) == 0 {
		return nil, nil // job not found
	}

	priority, _ := strconv.Atoi(jobMap["priority"])
	retries, _ := strconv.Atoi(jobMap["retries"])
	maxRetries, _ := strconv.Atoi(jobMap["max_retries"])
	createdAt, _ := strconv.ParseInt(jobMap["created_at"], 10, 64)
	runAt, _ := strconv.ParseInt(jobMap["run_at"], 10, 64)

	job := &queue.Job{
		ID:         jobMap["id"],
		Type:       jobMap["type"],
		Payload:    jobMap["payload"],
		Status:     jobMap["status"],
		Priority:   priority,
		Retries:    retries,
		MaxRetries: maxRetries,
		CreatedAt:  createdAt,
		RunAt:      runAt,
	}
	return job, nil
}

// hgetall is a private helper method for retrieving Redis hash values
func (s *RedisStore) hgetall(ctx context.Context, key string) (map[string]string, error) {
	return s.client.HGetAll(ctx, key).Result()
}

func (s *RedisStore) UpdateJobStatus(ctx context.Context, jobID string, status string) error {
	// Implementation to update the status of a job in the Redis hash, allowing for tracking the state of the job as it progresses through the queue system.
	return s.client.HSet(ctx, "job:"+jobID, "status", status).Err()
}

func (s *RedisStore) GetPendingCount(ctx context.Context) (int64, error) {
	// get the count of pending jobs from the Redis sorted set, allowing for tracking the number of jobs that are waiting to be processed.
	return s.client.ZCard(ctx, "pending_jobs").Result()
}

func (s *RedisStore) GetInProgressCount(ctx context.Context) (int64, error) {
	// get the count of in-progress jobs from the Redis set, allowing for tracking the number of jobs that are currently being processed by workers.
	return s.client.SCard(ctx, "running_jobs").Result()
}

func (s *RedisStore) GetCompletedCount(ctx context.Context) (int64, error) {
	// get the count of completed jobs from the Redis set, allowing for tracking the number of jobs that have been successfully processed and acknowledged by workers.
	return s.client.SCard(ctx, "completed_jobs").Result()
}

func (s *RedisStore) GetFailedCount(ctx context.Context) (int64, error) {
	// get the count of failed jobs from the Redis list, allowing for tracking the number of jobs that have failed after reaching the maximum number of retries.
	return s.client.LLen(ctx, "failed_jobs").Result()
}

func (s *RedisStore) AddToCompleted(ctx context.Context, jobID string) error {
	// track completed job with timestamp for throughput calculation.
	// Add to completed set for total count
	s.client.SAdd(ctx, "completed_jobs", jobID)

	// Add to time-windowed sorted set (score = timestamp in seconds)
	now := time.Now().Unix()
	return s.client.ZAdd(ctx, "completed_jobs_timed", redis.Z{
		Score:  float64(now),
		Member: jobID,
	}).Err()
}

func (s *RedisStore) GetThroughput(ctx context.Context) (float64, error) {
	// Calculate throughput as jobs processed per second in the last minute.
	// Uses time-windowed sorted set to track job completion timestamps.
	oneMinuteAgo := time.Now().Add(-1 * time.Minute).Unix()

	// Count jobs completed in the last 60 seconds
	count, err := s.client.ZCount(ctx, "completed_jobs_timed",
		fmt.Sprint(oneMinuteAgo), "+inf").Result()
	if err != nil {
		return 0, err
	}

	// Return jobs per second
	return float64(count) / 60.0, nil
}




