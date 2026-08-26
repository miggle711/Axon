package operations

import (
	"context"
	"testing"


	jobpkg "axon-queue"
)

// MockJobStore implements store.JobStore for testing
type MockJobStore struct {
	jobs map[string]*jobpkg.Job
}

func NewMockJobStore() *MockJobStore {
	return &MockJobStore{
		jobs: make(map[string]*jobpkg.Job),
	}
}

func (m *MockJobStore) StoreJob(ctx context.Context, job *jobpkg.Job) error {
	m.jobs[job.ID] = job
	return nil
}

func (m *MockJobStore) GetJob(ctx context.Context, jobID string) (*jobpkg.Job, error) {
	return m.jobs[jobID], nil
}

func (m *MockJobStore) UpdateJobStatus(ctx context.Context, jobID string, status string) error {
	if job, ok := m.jobs[jobID]; ok {
		job.Status = status
	}
	return nil
}

func (m *MockJobStore) IncrementRetries(ctx context.Context, jobID string) (int, error) {
	job, ok := m.jobs[jobID]
	if !ok {
		return 0, nil
	}
	job.Retries++
	return job.Retries, nil
}

// MockQueueOperations implements store.QueueOperations for testing
type MockQueueOperations struct {
	pending   []*jobpkg.Job
	running   map[string]*jobpkg.Job
	completed map[string]*jobpkg.Job
	failed    map[string]*jobpkg.Job
}

func NewMockQueueOperations() *MockQueueOperations {
	return &MockQueueOperations{
		pending:   make([]*jobpkg.Job, 0),
		running:   make(map[string]*jobpkg.Job),
		completed: make(map[string]*jobpkg.Job),
		failed:    make(map[string]*jobpkg.Job),
	}
}

func (m *MockQueueOperations) AddToPending(ctx context.Context, job *jobpkg.Job, priority int) error {
	m.pending = append(m.pending, job)
	return nil
}

func (m *MockQueueOperations) PopFromPending(ctx context.Context) (*jobpkg.Job, error) {
	if len(m.pending) == 0 {
		return nil, nil
	}
	job := m.pending[0]
	m.pending = m.pending[1:]
	return job, nil
}

func (m *MockQueueOperations) AddToRunning(ctx context.Context, job *jobpkg.Job) error {
	m.running[job.ID] = job
	return nil
}

func (m *MockQueueOperations) RemoveFromRunning(ctx context.Context, jobID string) error {
	delete(m.running, jobID)
	return nil
}

func (m *MockQueueOperations) AddToFailed(ctx context.Context, job *jobpkg.Job, reason string) error {
	m.failed[job.ID] = job
	return nil
}

func (m *MockQueueOperations) AddToCompleted(ctx context.Context, jobID string) error {
	// For mock purposes, just track that it was completed
	return nil
}

// MockQueueMetrics implements store.QueueMetrics for testing
type MockQueueMetrics struct {
	pending   int64
	running   int64
	completed int64
	failed    int64
	throughput float64
}

func NewMockQueueMetrics() *MockQueueMetrics {
	return &MockQueueMetrics{}
}

func (m *MockQueueMetrics) GetPendingCount(ctx context.Context) (int64, error) {
	return m.pending, nil
}

func (m *MockQueueMetrics) GetInProgressCount(ctx context.Context) (int64, error) {
	return m.running, nil
}

func (m *MockQueueMetrics) GetCompletedCount(ctx context.Context) (int64, error) {
	return m.completed, nil
}

func (m *MockQueueMetrics) GetFailedCount(ctx context.Context) (int64, error) {
	return m.failed, nil
}

func (m *MockQueueMetrics) GetThroughput(ctx context.Context) (float64, error) {
	return m.throughput, nil
}

// Tests

func TestEnqueueStoresJob(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	job := &jobpkg.Job{
		ID:       "test-job-1",
		Type:     "email",
		Payload:  `{"to":"test@example.com"}`,
		Status:   "pending",
		Priority: 1,
	}

	err := q.Enqueue(ctx, job)
	if err != nil {
		t.Fatalf("Enqueue failed: %v", err)
	}

	// Verify job was stored
	storedJob, _ := mockStore.GetJob(ctx, job.ID)
	if storedJob == nil {
		t.Error("Job was not stored")
	}
	if storedJob.ID != job.ID {
		t.Errorf("Expected job ID %s, got %s", job.ID, storedJob.ID)
	}
}

func TestDequeueMovesJobToRunning(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	job := &jobpkg.Job{
		ID:       "test-job-1",
		Type:     "email",
		Status:   "pending",
		Priority: 1,
	}

	// First enqueue
	q.Enqueue(ctx, job)

	// Then dequeue
	dequeuedJob, err := q.Dequeue(ctx)
	if err != nil {
		t.Fatalf("Dequeue failed: %v", err)
	}

	if dequeuedJob == nil {
		t.Fatal("Dequeued job is nil")
	}

	// Verify job is in running (mock)
	if _, ok := mockOps.running[job.ID]; !ok {
		t.Error("Job was not added to running set")
	}
}

func TestAckMarksJobCompleted(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	jobID := "test-job-1"
	job := &jobpkg.Job{
		ID:     jobID,
		Type:   "email",
		Status: "running",
	}

	mockStore.StoreJob(ctx, job)
	mockOps.AddToRunning(ctx, job)

	err := q.Ack(ctx, jobID)
	if err != nil {
		t.Fatalf("Ack failed: %v", err)
	}

	// Verify job is no longer in running
	if _, ok := mockOps.running[jobID]; ok {
		t.Error("Job was not removed from running set")
	}

	// Verify status was updated
	updatedJob, _ := mockStore.GetJob(ctx, jobID)
	if updatedJob.Status != "completed" {
		t.Errorf("Expected status 'completed', got '%s'", updatedJob.Status)
	}
}

func TestNackMarksJobFailed_NoRetriesConfigured(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	jobID := "test-job-1"
	job := &jobpkg.Job{
		ID:         jobID,
		Type:       "email",
		Status:     "running",
		MaxRetries: 0, // no retries allowed, so the first Nack fails it permanently
	}

	mockStore.StoreJob(ctx, job)
	mockOps.AddToRunning(ctx, job)

	err := q.Nack(ctx, jobID, "timeout")
	if err != nil {
		t.Fatalf("Nack failed: %v", err)
	}

	// Verify job is no longer in running
	if _, ok := mockOps.running[jobID]; ok {
		t.Error("Job was not removed from running set")
	}

	// Verify job is in failed
	if _, ok := mockOps.failed[jobID]; !ok {
		t.Error("Job was not added to failed set")
	}

	// Verify status was updated
	updatedJob, _ := mockStore.GetJob(ctx, jobID)
	if updatedJob.Status != "failed" {
		t.Errorf("Expected status 'failed', got '%s'", updatedJob.Status)
	}
}

func TestNackRetriesJobWhenRetriesRemain(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	jobID := "test-job-1"
	job := &jobpkg.Job{
		ID:         jobID,
		Type:       "email",
		Status:     "running",
		Priority:   3,
		MaxRetries: 3,
	}

	mockStore.StoreJob(ctx, job)
	mockOps.AddToRunning(ctx, job)

	err := q.Nack(ctx, jobID, "rate limited")
	if err != nil {
		t.Fatalf("Nack failed: %v", err)
	}

	// Should NOT be in the failed set - retries remain
	if _, ok := mockOps.failed[jobID]; ok {
		t.Error("Job should not be in failed set while retries remain")
	}

	// Should be back in pending
	if len(mockOps.pending) != 1 || mockOps.pending[0].ID != jobID {
		t.Errorf("expected job to be re-added to pending, got pending=%v", mockOps.pending)
	}

	// Retry count should have incremented
	updatedJob, _ := mockStore.GetJob(ctx, jobID)
	if updatedJob.Retries != 1 {
		t.Errorf("expected Retries=1, got %d", updatedJob.Retries)
	}
	if updatedJob.Status != "pending" {
		t.Errorf("expected status 'pending', got %q", updatedJob.Status)
	}
}

func TestNackFailsJobOnceRetriesExhausted(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	jobID := "test-job-1"
	job := &jobpkg.Job{
		ID:         jobID,
		Type:       "email",
		Status:     "running",
		MaxRetries: 2,
	}
	mockStore.StoreJob(ctx, job)

	// Nack it MaxRetries times: each one increments Retries and, while
	// the new count is still < MaxRetries, re-enqueues instead of
	// failing. The MaxRetries-th Nack is the one where the incremented
	// count finally reaches MaxRetries and the job fails permanently.
	for i := 0; i < job.MaxRetries; i++ {
		mockOps.AddToRunning(ctx, job)
		if err := q.Nack(ctx, jobID, "still failing"); err != nil {
			t.Fatalf("Nack #%d failed: %v", i+1, err)
		}

		updatedJob, _ := mockStore.GetJob(ctx, jobID)
		isLastAttempt := i == job.MaxRetries-1
		_, failed := mockOps.failed[jobID]
		if failed != isLastAttempt {
			t.Errorf("Nack #%d: expected failed=%v, got %v (Retries=%d)", i+1, isLastAttempt, failed, updatedJob.Retries)
		}
	}

	updatedJob, _ := mockStore.GetJob(ctx, jobID)
	if updatedJob.Status != "failed" {
		t.Errorf("expected status 'failed', got %q", updatedJob.Status)
	}
	if updatedJob.Retries != job.MaxRetries {
		t.Errorf("expected Retries=%d once permanently failed, got %d", job.MaxRetries, updatedJob.Retries)
	}
}

func TestGetJobStatus(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	jobID := "test-job-1"
	job := &jobpkg.Job{
		ID:     jobID,
		Type:   "email",
		Status: "pending",
	}

	mockStore.StoreJob(ctx, job)

	status, err := q.GetJobStatus(ctx, jobID)
	if err != nil {
		t.Fatalf("GetJobStatus failed: %v", err)
	}

	if status != "pending" {
		t.Errorf("Expected status 'pending', got '%s'", status)
	}
}

func TestGetJobStatusNotFound(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	status, err := q.GetJobStatus(ctx, "nonexistent")
	if err != nil {
		t.Fatalf("GetJobStatus failed: %v", err)
	}

	if status != "not found" {
		t.Errorf("Expected status 'not found', got '%s'", status)
	}
}

func TestGetStats(t *testing.T) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	// Set mock metrics
	mockMetrics.pending = 5
	mockMetrics.running = 2
	mockMetrics.completed = 10
	mockMetrics.failed = 1
	mockMetrics.throughput = 0.5

	stats, err := q.GetStats(ctx)
	if err != nil {
		t.Fatalf("GetStats failed: %v", err)
	}

	tests := []struct {
		name     string
		got      int64
		want     int64
	}{
		{"pending", stats.PendingJobs, 5},
		{"in_progress", stats.InProgressJobs, 2},
		{"completed", stats.CompletedJobs, 10},
		{"failed", stats.FailedJobs, 1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.got != tt.want {
				t.Errorf("Expected %d, got %d", tt.want, tt.got)
			}
		})
	}

	if stats.Throughput != 0.5 {
		t.Errorf("Expected throughput 0.5, got %f", stats.Throughput)
	}
}

// Table-driven test example
func TestEnqueueMultipleJobs(t *testing.T) {
	tests := []struct {
		name     string
		jobID    string
		jobType  string
		priority int
	}{
		{"email job", "job-1", "email", 2},
		{"sms job", "job-2", "sms", 1},
		{"webhook job", "job-3", "webhook", 3},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ctx := context.Background()
			mockStore := NewMockJobStore()
			mockOps := NewMockQueueOperations()
			mockMetrics := NewMockQueueMetrics()
			q := NewQueue(mockStore, mockOps, mockMetrics)

			job := &jobpkg.Job{
				ID:       tt.jobID,
				Type:     tt.jobType,
				Priority: tt.priority,
				Status:   "pending",
			}

			err := q.Enqueue(ctx, job)
			if err != nil {
				t.Fatalf("Enqueue failed: %v", err)
			}

			stored, _ := mockStore.GetJob(ctx, tt.jobID)
			if stored == nil {
				t.Error("Job not stored")
			}
			if stored.Type != tt.jobType {
				t.Errorf("Expected type %s, got %s", tt.jobType, stored.Type)
			}
		})
	}
}

// Benchmark test
func BenchmarkEnqueue(b *testing.B) {
	ctx := context.Background()
	mockStore := NewMockJobStore()
	mockOps := NewMockQueueOperations()
	mockMetrics := NewMockQueueMetrics()
	q := NewQueue(mockStore, mockOps, mockMetrics)

	job := &jobpkg.Job{
		ID:       "bench-job",
		Type:     "email",
		Priority: 1,
		Status:   "pending",
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		job.ID = "bench-job-" + string(rune(i))
		q.Enqueue(ctx, job)
	}
}
