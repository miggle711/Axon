package store


import (
	"context"
	"github.com/redis/go-redis/v9"
	"strconv"
	"time"
	"axon-queue"
	"fmt"
)

type JobStore interface {
	// standard interface for interacting with underlying storage
	// prevents tight coupling between the queue implementation and the specific storage mechanism
	// so the queue does not need to know about the details of how jobs are stored and retrieved, allowing for potential future support of other storage backends without changing the queue interface.
	StoreJob(job *queue.Job) error
	GetJob(jobID string) (*queue.Job, error)
	HSet(key string, data map[string]interface{}) error
	HGetAll(key string) (map[string]string, error)
	UpdateJobStatus(jobID string, status string) error
	GetPendingCount(ctx context.Context) (int64, error)
	GetInProgressCount(ctx context.Context) (int64, error)
	GetCompletedCount(ctx context.Context) (int64, error)
	GetFailedCount(ctx context.Context) (int64, error)
	GetThroughput(ctx context.Context) (float64, error)
	AddToCompleted(ctx context.Context, jobID string) error
}


type QueueOperations interface {
	AddToPending(job *queue.Job, priority int) error // adds a job to the pending queue with a specified priority, allowing for efficient retrieval of jobs based on their scheduling and prioritization
	PopFromPending(ctx context.Context) (*queue.Job, error) // pops the highest priority job from the pending queue and returns it for processing
	AddToRunning(job *queue.Job) error // add to set
	RemoveFromRunning(jobID string) error // remove from set
	AddToFailed(job *queue.Job, reason string) error // add to set
}
	

type RedisStore struct {
	// RedisStore is a concrete implementation of the Store interface that uses Redis as the underlying storage mechanism for managing job data in the queue system.
	client *redis.Client
}

func (s *RedisStore) AddToPending(job *queue.Job, priority int) error {
	// Implementation to add a job to the pending queue in Redis, using a sorted set to manage job prioritization based on the provided priority level.
	return s.client.ZAdd(context.Background(), "pending_jobs", redis.Z{
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
	return s.GetJob(jobID) // retrieve the job details using the job ID
}

func (s *RedisStore) AddToRunning(job *queue.Job) error {
	// Implementation to add a job to the running set in Redis, allowing for tracking of jobs that are currently being processed by workers.
	return s.client.SAdd(context.Background(), "running_jobs", job.ID).Err()
}

func (s *RedisStore) RemoveFromRunning(jobID string) error {
	// Implementation to remove a job from the running set in Redis, allowing for tracking of jobs that have completed processing or have been acknowledged by workers.
	return s.client.SRem(context.Background(), "running_jobs", jobID).Err()
}

func (s *RedisStore) AddToFailed(job *queue.Job, reason string) error {
	// Implementation to add a job to the failed set in Redis, allowing for tracking of jobs that have failed after reaching the maximum number of retries.
	failedEntry := fmt.Sprintf("%s:%s", job.ID, reason)
  	return s.client.LPush(context.Background(), "failed_jobs", failedEntry).Err()
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


func (s *RedisStore) StoreJob(job *queue.Job) error {
	// Implementation to store the job details in the Redis hash, allowing for quick retrieval and updates of job information.
	// use HSET command to store the job details in the Redis hash, with the job ID as the key and the job data as the value. This allows for efficient retrieval and updates of job information based on the unique job ID.

	// convert the Job struct to a hash map to be stored as a Redis Hash
	jobMap := map[string]interface{}{
		"id":          job.ID, 
		"type":        job.Type,
		"payload":     job.Payload, // store the payload as a string, since it is already a JSON string in the Job struct
		"status":      job.Status,
		"priority":    job.Priority,
		"retries":     job.Retries,
		"max_retries": job.MaxRetries,
		"created_at":  strconv.FormatInt(job.CreatedAt, 10), // store timestamp as a string to avoid issues with Redis hash storage
		"run_at":      strconv.FormatInt(job.RunAt, 10),     // store timestamp as a string to avoid issues with Redis hash storage
	}

	err := s.HSet("job:"+job.ID, jobMap)
	if err != nil {
		return err
	}
	return nil
}

func (s *RedisStore) HSet(key string, values map[string]interface{}) error { // interface{} allows us to store values of any type in the Redis hash
	// Implementation to set multiple fields in a Redis hash using the HSET command, allowing for efficient storage of job details.
	// The key parameter represents the Redis hash key (e.g., "job:<job_id>"), and the values parameter is a map containing the field-value pairs to be stored in the hash.
	return s.client.HSet(context.Background(), key, values).Err()
}

func (s *RedisStore) GetJob(jobID string) (*queue.Job, error) {
	// Implementation to retrieve job details from the Redis hash using the job ID as the key.
	jobMap, err := s.HGetAll("job:" + jobID)
	if err != nil {
		return nil, err
	}
	if len(jobMap) == 0 {
		return nil, nil // job not found
	}

	// strconv.Atoi and time.Parse to convert string values back to their original types (e.g., int, time.Time) when retrieving job details from the Redis hash, ensuring that the job data is correctly reconstructed for processing.
	priority, _ := strconv.Atoi(jobMap["priority"]) // convert priority back to int
	retries, _ := strconv.Atoi(jobMap["retries"]) // convert retries back to int
	maxRetries, _ := strconv.Atoi(jobMap["max_retries"]) // convert max_retries back to int
	createdAt, _ := time.Parse(time.RFC3339, jobMap["created_at"]) // convert created_at back to time.Time
	runAt, _ := time.Parse(time.RFC3339, jobMap["run_at"]) // convert run_at back to time.Time

	job := &queue.Job{
		ID:         jobMap["id"],
		Type:       jobMap["type"],
		Payload:    jobMap["payload"], // payload is stored as a string, so we can directly assign it back to the Job struct
		Status:     jobMap["status"],
		Priority:   priority,
		Retries:    retries,
		MaxRetries: maxRetries,
		CreatedAt:  createdAt.Unix(), // convert time to timestamp
		RunAt:      runAt.Unix(),     // convert time to timestamp
	}
	return job, nil


}

func (s *RedisStore) HGetAll(key string) (map[string]string, error) {
	// retrieve all fields and values from a Redis hash using the HGETALL command, allowing for efficient retrieval of job details based on the job ID.
    return s.client.HGetAll(context.Background(), key).Result()
	}

func (s *RedisStore) UpdateJobStatus(jobID string, status string) error {
	// Implementation to update the status of a job in the Redis hash, allowing for tracking the state of the job as it progresses through the queue system.
	return s.client.HSet(context.Background(), "job:"+jobID, "status", status).Err()
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




