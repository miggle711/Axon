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

	permanentlyFailed, err := q.Nack(ctx, dequeuedJob.ID, "timeout error")
	if err != nil {
		t.Fatalf("Nack failed: %v", err)
	}
	if !permanentlyFailed {
		t.Error("expected permanentlyFailed=true with MaxRetries=0")
	}
	t.Log("✓ Job nacked (marked as failed)")

	// Verify status changed to failed
	status, _ := q.GetJobStatus(ctx, dequeuedJob.ID)
	if status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", status)
	}
	t.Log("✓ Status verified as failed")
}

func TestNackRetriesJobIntegration(t *testing.T) {
	if testing.Short() {
		t.Skip("Skipping integration test")
	}

	ctx := context.Background()

	redisStore, err := store.NewRedisStore("redis://localhost:6379")
	if err != nil {
		t.Skipf("Could not connect to Redis: %v", err)
	}

	q := NewQueue(redisStore, redisStore, redisStore)

	job := &jobpkg.Job{
		ID:         "integration-test-nack-retry",
		Type:       "email",
		Status:     "pending",
		Priority:   1,
		MaxRetries: 2,
	}

	if err := q.Enqueue(ctx, job); err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}
	t.Log("✓ Job enqueued")

	dequeuedJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	// First Nack: one retry remains (MaxRetries=2), so this should
	// re-enqueue rather than permanently fail.
	permanentlyFailed, err := q.Nack(ctx, dequeuedJob.ID, "transient error")
	if err != nil {
		t.Fatalf("Nack (1st) failed: %v", err)
	}
	if permanentlyFailed {
		t.Error("expected permanentlyFailed=false while retries remain")
	}
	status, _ := q.GetJobStatus(ctx, dequeuedJob.ID)
	if status != "pending" {
		t.Fatalf("expected status 'pending' after first Nack with retries remaining, got %q", status)
	}
	t.Log("✓ First Nack re-enqueued the job instead of failing it")

	// It must actually be dequeuable again.
	redequeued, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("re-Dequeue failed: %v", err)
	}
	if redequeued == nil || redequeued.ID != dequeuedJob.ID {
		t.Fatalf("expected to re-dequeue %s, got %+v", dequeuedJob.ID, redequeued)
	}
	if redequeued.Retries != 1 {
		t.Errorf("expected Retries=1 after one Nack, got %d", redequeued.Retries)
	}
	t.Log("✓ Job was re-dequeued with an incremented retry count")

	// Second Nack: retries now reach MaxRetries, so this should
	// permanently fail.
	permanentlyFailed, err = q.Nack(ctx, redequeued.ID, "transient error again")
	if err != nil {
		t.Fatalf("Nack (2nd) failed: %v", err)
	}
	if !permanentlyFailed {
		t.Error("expected permanentlyFailed=true once retries are exhausted")
	}
	finalStatus, _ := q.GetJobStatus(ctx, redequeued.ID)
	if finalStatus != "failed" {
		t.Errorf("expected status 'failed' once retries are exhausted, got %q", finalStatus)
	}
	t.Log("✓ Second Nack permanently failed the job once retries were exhausted")
}
