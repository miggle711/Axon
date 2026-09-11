//go:build integration

package engine

import (
	"context"
	"testing"
	"time"
)

// Integration test - requires Redis running.
// Run with: go test -tags integration ./...
//
// Covers #53's storage layer directly against real Redis, since
// RedisRunStore has never had dedicated tests before this - it's only
// ever been exercised indirectly through live manual testing.

func newIntegrationRunStore(t *testing.T) *RedisRunStore {
	t.Helper()
	store, err := NewRedisRunStore("redis://localhost:6379")
	if err != nil {
		t.Skipf("could not connect to Redis: %v", err)
	}
	return store
}

func TestRedisRunStore_ListRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	store := newIntegrationRunStore(t)
	ctx := context.Background()

	// Use a unique-ish agent name per test run so this test doesn't
	// collide with data left behind by a prior run against the same
	// Redis instance.
	agentA := "list_runs_test_agent_a_" + time.Now().Format("150405.000000")
	agentB := "list_runs_test_agent_b_" + time.Now().Format("150405.000000")

	base := time.Now()
	runs := []*Run{
		{ID: "lr-1", AgentName: agentA, Status: "completed", CreatedAt: base.Add(1 * time.Second)},
		{ID: "lr-2", AgentName: agentA, Status: "failed", CreatedAt: base.Add(2 * time.Second)},
		{ID: "lr-3", AgentName: agentB, Status: "completed", CreatedAt: base.Add(3 * time.Second)},
		{ID: "lr-4", AgentName: agentA, Status: "completed", CreatedAt: base.Add(4 * time.Second), ParentRunID: "lr-1"}, // child run, must not appear in any list
	}
	for _, run := range runs {
		if err := store.SaveRun(ctx, run); err != nil {
			t.Fatalf("SaveRun(%s) failed: %v", run.ID, err)
		}
	}

	t.Run("unfiltered lists all top-level runs newest first", func(t *testing.T) {
		got, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA})
		if err != nil {
			t.Fatalf("ListRuns failed: %v", err)
		}
		if len(got) != 2 {
			t.Fatalf("expected 2 runs for %s, got %d: %v", agentA, len(got), got)
		}
		if got[0].ID != "lr-2" || got[1].ID != "lr-1" {
			t.Errorf("expected newest first [lr-2, lr-1], got [%s, %s]", got[0].ID, got[1].ID)
		}
	})

	t.Run("agent and status filters combine", func(t *testing.T) {
		got, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "completed"})
		if err != nil {
			t.Fatalf("ListRuns failed: %v", err)
		}
		if len(got) != 1 || got[0].ID != "lr-1" {
			t.Fatalf("expected only lr-1, got %v", got)
		}
	})

	t.Run("child runs never appear", func(t *testing.T) {
		got, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA})
		if err != nil {
			t.Fatalf("ListRuns failed: %v", err)
		}
		for _, run := range got {
			if run.ID == "lr-4" {
				t.Error("expected child run lr-4 to be excluded from the list, but it was present")
			}
		}
	})

	t.Run("status index follows a run across a status transition", func(t *testing.T) {
		run := &Run{ID: "lr-1", AgentName: agentA, Status: "completed", CreatedAt: base.Add(1 * time.Second)}
		run.Status = "failed"
		if err := store.SaveRun(ctx, run); err != nil {
			t.Fatalf("SaveRun failed: %v", err)
		}

		completed, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "completed"})
		if err != nil {
			t.Fatalf("ListRuns(completed) failed: %v", err)
		}
		for _, r := range completed {
			if r.ID == "lr-1" {
				t.Error("expected lr-1 to no longer be in the completed index after transitioning to failed")
			}
		}

		failed, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "failed"})
		if err != nil {
			t.Fatalf("ListRuns(failed) failed: %v", err)
		}
		found := false
		for _, r := range failed {
			if r.ID == "lr-1" {
				found = true
			}
		}
		if !found {
			t.Error("expected lr-1 to be in the failed index after transitioning")
		}
	})
}
