// +build integration

package operations

import (
	"context"
	"testing"

	jobpkg "axon-queue"
	"axon-queue/internal/store"
)

// Integration test - requires Redis running
// Run with: go test -tags integration ./...

func TestFullJobLifecycleIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()

	// Connect to Redis
	redisStore, err := store.NewRedisStore("redis://localhost:6379")
	if err != nil {
		t.Skipf("Could not connect to Redis: %v", err)
	}

	q := NewQueue(redisStore, redisStore, redisStore)

	// Test data
	jobID := "integration-test-job-1"
	job := &jobpkg.Job{
		ID:         jobID,
		Type:       "email",
		Payload:    `{"to":"test@example.com"}`,
		Status:     "pending",
		Priority:   1,
		Retries:    0,
		MaxRetries: 3,
	}

	// Step 1: Enqueue
	err = q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	t.Log("✓ Job enqueued")

	// Step 2: Get initial stats
	stats, err := q.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if stats.PendingJobs < 1 {
		t.Errorf("Expected at least 1 pending job, got %d", stats.PendingJobs)
	}
	t.Logf("✓ Stats show pending jobs: %d", stats.PendingJobs)

	// Step 3: Dequeue
	dequeuedJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	if dequeuedJob == nil {
		t.Fatal("Dequeued job is nil")
	}

	if dequeuedJob.ID != jobID {
		t.Errorf("Expected job ID %s, got %s", jobID, dequeuedJob.ID)
	}
	t.Log("✓ Job dequeued")

	// Step 4: Get status while running
	status, err := q.GetJobStatus(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJobStatus failed: %v", err)
	}
	t.Logf("✓ Job status: %s", status)

	// Step 5: Ack (mark complete)
	err = q.Ack(ctx, jobID)
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}
	t.Log("✓ Job acknowledged (completed)")

	// Step 6: Verify final status
	finalStatus, err := q.GetJobStatus(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJobStatus failed: %v", err)
	}

	if finalStatus != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", finalStatus)
	}
	t.Log("✓ Final status verified as completed")

	// Step 7: Check final stats
	finalStats, err := q.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	if finalStats.CompletedJobs < 1 {
		t.Errorf("Expected at least 1 completed job, got %d", finalStats.CompletedJobs)
	}
	t.Logf("✓ Final stats - Completed: %d, Throughput: %f/sec",
		finalStats.CompletedJobs, finalStats.Throughput)
}

func TestNackJobIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()

	// Connect to Redis
	redisStore, err := store.NewRedisStore("redis://localhost:6379")
	if err != nil {
		t.Skipf("Could not connect to Redis: %v", err)
	}

	q := NewQueue(redisStore, redisStore, redisStore)

	job := &jobpkg.Job{
		ID:         "integration-test-nack",
		Type:       "email",
		Status:     "pending",
		Priority:   1,
	}

	// Enqueue, dequeue, then nack
	q.Enqueue(ctx, job)
	dequeuedJob, _ := q.Dequeue(ctx)

	err = q.Nack(ctx, dequeuedJob.ID, "timeout error")
	if err != nil {
		t.Fatalf("Nack failed: %v", err)
	}
	t.Log("✓ Job nacked (marked as failed)")

	// Verify status changed to failed
	status, _ := q.GetJobStatus(ctx, dequeuedJob.ID)
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}
	t.Log("✓ Status verified as failed")
}
