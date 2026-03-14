package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	jobpkg "axon-queue"
	"axon-queue/internal/operations"
	"axon-queue/internal/store"
)

// Run with: 
// cd queue                                                                                                                                                                         
// go run ./cmd/main.go

func main() {
	// Define CLI flags
	redisURL := flag.String("redis", "redis://localhost:6379", "Redis URL")
	action := flag.String("action", "demo", "Action: demo, enqueue, dequeue, status, stats")
	jobID := flag.String("job-id", "", "Job ID for status lookup")
	jobType := flag.String("job-type", "email", "Job type for enqueuing")
	jobPayload := flag.String("payload", `{"to":"user@example.com"}`, "Job payload (JSON)")
	priority := flag.Int("priority", 1, "Job priority")

	flag.Parse()

	// Connect to Redis
	redisStore, err := store.NewRedisStore(*redisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Create queue with the three interface implementations
	q := operations.NewQueue(redisStore, redisStore, redisStore)
	ctx := context.Background()

	switch *action {
	case "demo":
		runDemo(ctx, q)

	case "enqueue":
		enqueueJob(ctx, q, *jobType, *jobPayload, *priority)

	case "dequeue":
		dequeueJob(ctx, q)

	case "status":
		if *jobID == "" {
			log.Fatal("--job-id required for status action")
		}
		getJobStatus(ctx, q, *jobID)

	case "stats":
		getStats(ctx, q)

	default:
		log.Fatalf("Unknown action: %s", *action)
	}
}

// runDemo demonstrates the complete job lifecycle
func runDemo(ctx context.Context, q *operations.Queue) {
	fmt.Println("=== Queue Demo ===")

	// 1. Enqueue jobs
	fmt.Println("1. Enqueuing jobs...")
	job1 := &jobpkg.Job{
		ID:         "demo-job-1",
		Type:       "email",
		Payload:    `{"to":"alice@example.com","subject":"Hello"}`,
		Status:     "pending",
		Priority:   2,
		Retries:    0,
		MaxRetries: 3,
		CreatedAt:  time.Now().Unix(),
		RunAt:      time.Now().Unix(),
	}

	job2 := &jobpkg.Job{
		ID:         "demo-job-2",
		Type:       "sms",
		Payload:    `{"phone":"+1234567890","message":"Hi"}`,
		Status:     "pending",
		Priority:   1,
		Retries:    0,
		MaxRetries: 3,
		CreatedAt:  time.Now().Unix(),
		RunAt:      time.Now().Unix(),
	}

	if err := q.Enqueue(ctx, job1); err != nil {
		log.Fatalf("Failed to enqueue job1: %v", err)
	}
	fmt.Printf("  Enqueued: %s (priority %d)\n", job1.ID, job1.Priority)

	if err := q.Enqueue(ctx, job2); err != nil {
		log.Fatalf("Failed to enqueue job2: %v", err)
	}
	fmt.Printf("  Enqueued: %s (priority %d)\n\n", job2.ID, job2.Priority)

	// 2. Get stats
	fmt.Println("2. Queue Stats after enqueue:")
	printStats(ctx, q)

	// 3. Dequeue (should get highest priority first)
	fmt.Println("\n3. Dequeuing job (should get highest priority)...")
	dequeuedJob, err := q.Dequeue(ctx)
	if err != nil {
		log.Fatalf("Failed to dequeue: %v", err)
	}
	fmt.Printf("  Dequeued: %s (type=%s, priority=%d)\n", dequeuedJob.ID, dequeuedJob.Type, dequeuedJob.Priority)
	fmt.Printf("    Status: %s → running\n\n", dequeuedJob.Status)

	// 4. Check job status while running
	fmt.Println("4. Job Status:")
	status, err := q.GetJobStatus(ctx, dequeuedJob.ID)
	if err != nil {
		log.Fatalf("Failed to get status: %v", err)
	}
	fmt.Printf("  %s: %s\n\n", dequeuedJob.ID, status)

	// 5. Get stats while processing
	fmt.Println("5. Queue Stats during processing:")
	printStats(ctx, q)

	// 6. Acknowledge completion
	fmt.Println("\n6. Job Acknowledgment (Ack)...")
	if err := q.Ack(ctx, dequeuedJob.ID); err != nil {
		log.Fatalf("Failed to ack: %v", err)
	}
	fmt.Printf("  ✓ Acknowledged: %s → completed\n\n", dequeuedJob.ID)

	// 7. Get stats after completion
	fmt.Println("7. Queue Stats after completion:")
	printStats(ctx, q)

	// 8. Dequeue remaining job
	fmt.Println("\n8. Dequeuing remaining job...")
	dequeuedJob2, err := q.Dequeue(ctx)
	if err != nil {
		log.Fatalf("Failed to dequeue: %v", err)
	}
	fmt.Printf("  Dequeued: %s (type=%s)\n\n", dequeuedJob2.ID, dequeuedJob2.Type)

	// 9. Negative acknowledgment (Nack)
	fmt.Println("9. Job Failure (Nack)...")
	if err := q.Nack(ctx, dequeuedJob2.ID, "API timeout"); err != nil {
		log.Fatalf("Failed to nack: %v", err)
	}
	fmt.Printf("   Nacked: %s → failed (reason: API timeout)\n\n", dequeuedJob2.ID)

	// 10. Final stats
	fmt.Println("10. Final Queue Stats:")
	printStats(ctx, q)
}

// enqueueJob enqueues a single job
func enqueueJob(ctx context.Context, q *operations.Queue, jobType, payload string, priority int) {
	job := &jobpkg.Job{
		ID:         fmt.Sprintf("job-%d", time.Now().UnixNano()),
		Type:       jobType,
		Payload:    payload,
		Status:     "pending",
		Priority:   priority,
		Retries:    0,
		MaxRetries: 3,
		CreatedAt:  time.Now().Unix(),
		RunAt:      time.Now().Unix(),
	}

	if err := q.Enqueue(ctx, job); err != nil {
		log.Fatalf("Failed to enqueue: %v", err)
	}

	fmt.Printf(" Enqueued job: %s\n", job.ID)
	fmt.Printf("  Type: %s\n", job.Type)
	fmt.Printf("  Priority: %d\n", job.Priority)
	fmt.Printf("  Payload: %s\n", job.Payload)
}

// dequeueJob dequeues and prints the next job
func dequeueJob(ctx context.Context, q *operations.Queue) {
	job, err := q.Dequeue(ctx)
	if err != nil {
		log.Fatalf("Failed to dequeue: %v", err)
	}

	if job == nil {
		fmt.Println("No jobs available in queue")
		return
	}

	fmt.Printf(" Dequeued job: %s\n", job.ID)
	fmt.Printf("  Type: %s\n", job.Type)
	fmt.Printf("  Priority: %d\n", job.Priority)
	fmt.Printf("  Status: pending → running\n")
	fmt.Printf("  Payload: %s\n", job.Payload)
}

// getJobStatus retrieves and prints job status
func getJobStatus(ctx context.Context, q *operations.Queue, jobID string) {
	status, err := q.GetJobStatus(ctx, jobID)
	if err != nil {
		log.Fatalf("Failed to get status: %v", err)
	}

	fmt.Printf("Job: %s\n", jobID)
	fmt.Printf("Status: %s\n", status)
}

// getStats retrieves and prints queue statistics
func getStats(ctx context.Context, q *operations.Queue) {
	printStats(ctx, q)
}

// printStats helper to format and print stats
func printStats(ctx context.Context, q *operations.Queue) {
	stats, err := q.GetStats(ctx)
	if err != nil {
		log.Fatalf("Failed to get stats: %v", err)
	}

	data, _ := json.MarshalIndent(stats, "  ", "  ")
	fmt.Printf("  %s\n", string(data))
}
