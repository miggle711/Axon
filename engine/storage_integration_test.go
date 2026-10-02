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

	// Suffix both the agent names and the run IDs with the same
	// per-run timestamp: a fixed run ID (like the "lr-1" this test used
	// to use) collides with whatever a prior run against the same
	// Redis instance already saved under that key, and the SaveRun
	// path in the "status index follows a transition" subtest below
	// would silently overwrite unrelated data left behind by an
	// earlier run rather than failing loudly.
	suffix := time.Now().Format("150405.000000")
	agentA := "list_runs_test_agent_a_" + suffix
	agentB := "list_runs_test_agent_b_" + suffix
	id1 := "lr-1-" + suffix
	id2 := "lr-2-" + suffix
	id3 := "lr-3-" + suffix
	id4 := "lr-4-" + suffix

	base := time.Now()
	runs := []*Run{
		{ID: id1, AgentName: agentA, Status: "completed", CreatedAt: base.Add(1 * time.Second)},
		{ID: id2, AgentName: agentA, Status: "failed", CreatedAt: base.Add(2 * time.Second)},
		{ID: id3, AgentName: agentB, Status: "completed", CreatedAt: base.Add(3 * time.Second)},
		{ID: id4, AgentName: agentA, Status: "completed", CreatedAt: base.Add(4 * time.Second), ParentRunID: id1}, // child run, must not appear in any list
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
		if got[0].ID != id2 || got[1].ID != id1 {
			t.Errorf("expected newest first [%s, %s], got [%s, %s]", id2, id1, got[0].ID, got[1].ID)
		}
	})

	t.Run("agent and status filters combine", func(t *testing.T) {
		got, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "completed"})
		if err != nil {
			t.Fatalf("ListRuns failed: %v", err)
		}
		if len(got) != 1 || got[0].ID != id1 {
			t.Fatalf("expected only %s, got %v", id1, got)
		}
	})

	t.Run("child runs never appear", func(t *testing.T) {
		got, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA})
		if err != nil {
			t.Fatalf("ListRuns failed: %v", err)
		}
		for _, run := range got {
			if run.ID == id4 {
				t.Errorf("expected child run %s to be excluded from the list, but it was present", id4)
			}
		}
	})

	t.Run("status index follows a run across a status transition", func(t *testing.T) {
		run := &Run{ID: id1, AgentName: agentA, Status: "completed", CreatedAt: base.Add(1 * time.Second)}
		run.Status = "failed"
		if err := store.SaveRun(ctx, run); err != nil {
			t.Fatalf("SaveRun failed: %v", err)
		}

		completed, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "completed"})
		if err != nil {
			t.Fatalf("ListRuns(completed) failed: %v", err)
		}
		for _, r := range completed {
			if r.ID == id1 {
				t.Errorf("expected %s to no longer be in the completed index after transitioning to failed", id1)
			}
		}

		failed, err := store.ListRuns(ctx, ListRunsOptions{AgentName: agentA, Status: "failed"})
		if err != nil {
			t.Fatalf("ListRuns(failed) failed: %v", err)
		}
		found := false
		for _, r := range failed {
			if r.ID == id1 {
				found = true
			}
		}
		if !found {
			t.Errorf("expected %s to be in the failed index after transitioning", id1)
		}
	})
}
