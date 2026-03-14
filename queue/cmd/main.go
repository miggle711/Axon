package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"time"

	jobpkg "axon-queue"
	"axon-queue/api"
	"axon-queue/internal/operations"
	"axon-queue/internal/store"
)

// Run with:
// cd queue
// go run ./cmd/main.go
// go run ./cmd/main.go -mode api -port 9000
// go run ./cmd/main.go -mode demo

func main() {
	// Define CLI flags
	redisURL := flag.String("redis", "redis://localhost:6379", "Redis URL")
	mode := flag.String("mode", "api", "Mode: api or demo")
	port := flag.String("port", "8080", "Server port (only for api mode)")

	flag.Parse()

	// Connect to Redis
	redisStore, err := store.NewRedisStore(*redisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	// Create queue with the three interface implementations
	q := operations.NewQueue(redisStore, redisStore, redisStore)

	switch *mode {
	case "api":
		// Start HTTP API server
		server := api.NewServer(q)
		log.Printf("Starting API server on port %s", *port)
		if err := server.Start(*port); err != nil {
			log.Fatalf("Failed to start server: %v", err)
		}

	case "demo":
		// Run CLI demo
		ctx := context.Background()
		runDemo(ctx, q)

	default:
		log.Fatalf("Unknown mode: %s (use 'api' or 'demo')", *mode)
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

// printStats helper to format and print stats
func printStats(ctx context.Context, q *operations.Queue) {
	stats, err := q.GetStats(ctx)
	if err != nil {
		log.Fatalf("Failed to get stats: %v", err)
	}

	data, _ := json.MarshalIndent(stats, "  ", "  ")
	fmt.Printf("  %s\n", string(data))
}
