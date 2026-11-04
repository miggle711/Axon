//go:build integration

package engine

import (
	"context"
	"encoding/json"
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

// TestRedisRunStore_AppendCompletedAndSkippedSteps covers #81's
// CompletedSteps/SkippedSteps split against real Redis, not just
// fakeRunStore - a review of the PR introducing this (#87) correctly
// flagged that the new RPUSH/LRange path, and SaveRun's omission of
// both fields from its own blob, were only exercised by the in-memory
// fake. This drives the real sequence a run actually goes through:
// SaveRun with no appends yet, several appends, then SaveRun again
// (mirroring OnStepCompleted calling SaveRun elsewhere in the same
// request), and confirms GetRun reflects every append across all of
// it.
func TestRedisRunStore_AppendCompletedAndSkippedSteps(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	store := newIntegrationRunStore(t)
	ctx := context.Background()

	id := "acs-" + time.Now().Format("150405.000000")
	run := &Run{ID: id, AgentName: "append_completed_skipped_test_agent", Status: "in_progress", CreatedAt: time.Now()}
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatalf("initial SaveRun failed: %v", err)
	}

	if err := store.AppendCompletedStep(ctx, id, "step_1"); err != nil {
		t.Fatalf("AppendCompletedStep(step_1) failed: %v", err)
	}
	if err := store.AppendSkippedSteps(ctx, id, []string{"on_false_step", "dependent_of_on_false_step"}); err != nil {
		t.Fatalf("AppendSkippedSteps failed: %v", err)
	}
	if err := store.AppendCompletedStep(ctx, id, "check"); err != nil {
		t.Fatalf("AppendCompletedStep(check) failed: %v", err)
	}

	// A real run calls SaveRun again after these appends (e.g.
	// finalizeStepCompletion does, to persist Status/UpdatedAt) - this
	// must not clobber what the appends already recorded, which is
	// exactly what the fix for #87's review findings guards against.
	run.Status = "completed"
	if err := store.SaveRun(ctx, run); err != nil {
		t.Fatalf("second SaveRun failed: %v", err)
	}

	got, err := store.GetRun(ctx, id)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected run to be found")
	}

	wantCompleted := []string{"step_1", "check"}
	if !equalStringSlices(got.CompletedSteps, wantCompleted) {
		t.Errorf("CompletedSteps = %v, want %v", got.CompletedSteps, wantCompleted)
	}
	wantSkipped := []string{"on_false_step", "dependent_of_on_false_step"}
	if !equalStringSlices(got.SkippedSteps, wantSkipped) {
		t.Errorf("SkippedSteps = %v, want %v", got.SkippedSteps, wantSkipped)
	}
}

// TestRedisRunStore_LegacyRunWithoutAppendListsKeepsBlobHistory covers
// the data-loss bug review flagged in #87: a run saved before
// CompletedSteps/SkippedSteps moved to their own list keys has that
// history living only in the main blob, with no :completed_steps/
// :skipped_steps list key at all. GetRun must read it from the blob
// in that case, not silently overwrite it with an empty list simply
// because the list key doesn't exist yet.
func TestRedisRunStore_LegacyRunWithoutAppendListsKeepsBlobHistory(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test")
	}
	store := newIntegrationRunStore(t)
	ctx := context.Background()

	id := "legacy-" + time.Now().Format("150405.000000")
	// Simulating the pre-#87 format directly: CompletedSteps/
	// SkippedSteps baked into the one blob, no separate list keys -
	// SaveRun itself can no longer produce this shape (it always
	// excludes both fields now), so this writes the raw key the way
	// the old SaveRun used to, to prove GetRun still reads it back
	// correctly rather than assuming every run already migrated.
	legacyRun := &Run{
		ID:             id,
		AgentName:      "legacy_test_agent",
		Status:         "completed",
		CreatedAt:      time.Now(),
		CompletedSteps: []string{"step_1", "step_2"},
		SkippedSteps:   []string{"step_3"},
	}
	data, err := json.Marshal(legacyRun)
	if err != nil {
		t.Fatalf("failed to marshal legacy run: %v", err)
	}
	if err := store.client.Set(ctx, runKey(id), data, 0).Err(); err != nil {
		t.Fatalf("failed to write legacy run blob directly: %v", err)
	}

	got, err := store.GetRun(ctx, id)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if got == nil {
		t.Fatal("expected run to be found")
	}

	wantCompleted := []string{"step_1", "step_2"}
	if !equalStringSlices(got.CompletedSteps, wantCompleted) {
		t.Errorf("CompletedSteps = %v, want %v (legacy blob data should not be overwritten by an empty list)", got.CompletedSteps, wantCompleted)
	}
	wantSkipped := []string{"step_3"}
	if !equalStringSlices(got.SkippedSteps, wantSkipped) {
		t.Errorf("SkippedSteps = %v, want %v (legacy blob data should not be overwritten by an empty list)", got.SkippedSteps, wantSkipped)
	}
}

func equalStringSlices(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
