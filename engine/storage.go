package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

// RunStore handles persistence for Run state.
type RunStore interface {
	SaveRun(ctx context.Context, run *Run) error
	GetRun(ctx context.Context, runID string) (*Run, error)

	// ListRuns returns top-level runs (ParentRunID == "", i.e. not a
	// child run spawned via agent_call - see #53) newest first,
	// optionally filtered by opts, paginated via opts.Limit/opts.Offset.
	ListRuns(ctx context.Context, opts ListRunsOptions) ([]*Run, error)

	// AppendEvent persists one new RunEvent for runID, in addition to
	// (not instead of) it living in that Run's own in-memory Events
	// slice. Events is append-only by nature, so writing each entry
	// here as it happens means SaveRun never needs to re-marshal the
	// whole, ever-growing Events slice on every call (#81) - a run
	// with n events would otherwise pay O(n) cost on every single
	// SaveRun just to re-persist event history nothing about that call
	// actually changed.
	AppendEvent(ctx context.Context, runID string, event RunEvent) error

	// AppendCompletedStep and AppendSkippedSteps are CompletedSteps/
	// SkippedSteps's equivalent of AppendEvent: both fields only ever
	// grow by appending (nothing ever removes an entry - see
	// doneSet's callers), so persisting each addition directly here
	// means SaveRun never needs to re-marshal either ever-growing
	// slice on every call, the same reasoning and the same #81 this
	// entry documents for Events.
	AppendCompletedStep(ctx context.Context, runID, stepID string) error
	AppendSkippedSteps(ctx context.Context, runID string, stepIDs []string) error
}

// ListRunsOptions filters and paginates a ListRuns call. AgentName and
// Status are ANDed together when both are set; either left as "" means
// "don't filter on this field". Limit defaults to 20 and is capped at
// 100 if given as 0 or above the cap, so a caller can't accidentally
// request an unbounded scan.
type ListRunsOptions struct {
	AgentName string
	Status    string
	Limit     int
	Offset    int
}

const (
	defaultListRunsLimit = 20
	maxListRunsLimit     = 100
)

type RedisRunStore struct {
	client *redis.Client
}

func NewRedisRunStore(redisURL string) (*RedisRunStore, error) {
	opts, err := redis.ParseURL(redisURL)
	if err != nil {
		return nil, err
	}
	client := redis.NewClient(opts)
	return &RedisRunStore{client: client}, nil
}

// SaveRun persists run and keeps three sorted-set indexes in sync so
// ListRuns never needs a KEYS scan (an anti-pattern in production
// Redis - O(N) and blocks the server): runs:by_time (every top-level
// run, for the unfiltered/default list), runs:by_agent:<agent_name>,
// and runs:by_status:<status>. Child runs (ParentRunID != "", spawned
// via agent_call) are persisted the same way but deliberately excluded
// from every index - "browse my runs" almost always means the
// top-level thing someone kicked off, not every internal sub-run an
// agent_call happened to spawn (#53).
//
// A run's Status can change across its lifetime (in_progress ->
// completed/failed), so every SaveRun call removes the run from all
// three possible status sets before adding it to the current one -
// cheap (ZREM is a no-op if the member isn't in that set) and always
// correct regardless of what the previous status was, without an
// extra read to find out.
//
// Four fields are deliberately excluded from this call's own blob
// (#81), each for its own reason rather than one general "trim the
// blob" pass:
//
//   - Steps is set once in CreateRun and never mutated again for the
//     life of a run, so rewriting it on every single SaveRun call
//     (potentially the largest field, the full agent definition) pays
//     an O(len(Steps)) marshal cost for data that provably never
//     changed. Written once to its own key via SetNX (a no-op past
//     the first call), not as part of this blob.
//   - Events, CompletedSteps, and SkippedSteps each only ever grow by
//     appending (nothing ever removes an entry from any of the
//     three), and each entry is already persisted the instant it's
//     created, via AppendEvent/AppendCompletedStep/AppendSkippedSteps
//     (called from orchestrator.go) - so re-marshaling the whole,
//     ever-growing slice here on every call would re-persist history
//     this call never touched.
//
// Both costs were real and measured: see BenchmarkOnStepCompleted_LinearChain
// in orchestrator_test.go, which is what surfaced this in the first
// place while verifying #11's unrelated fix.
func (s *RedisRunStore) SaveRun(ctx context.Context, run *Run) error {
	blob := *run // shallow copy: slices/maps still point at run's own backing data, fine since nothing here mutates them
	blob.Steps = nil
	blob.Events = nil
	blob.CompletedSteps = nil
	blob.SkippedSteps = nil

	data, err := json.Marshal(&blob)
	if err != nil {
		return err
	}

	stepsData, err := json.Marshal(run.Steps)
	if err != nil {
		return err
	}

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, runKey(run.ID), data, 0)
	pipe.SetNX(ctx, runStepsKey(run.ID), stepsData, 0)

	if run.ParentRunID == "" {
		score := float64(run.CreatedAt.UnixNano())
		pipe.ZAdd(ctx, runsByTimeKey(), redis.Z{Score: score, Member: run.ID})
		pipe.ZAdd(ctx, runsByAgentKey(run.AgentName), redis.Z{Score: score, Member: run.ID})

		for _, status := range []string{"in_progress", "completed", "failed"} {
			pipe.ZRem(ctx, runsByStatusKey(status), run.ID)
		}
		pipe.ZAdd(ctx, runsByStatusKey(run.Status), redis.Z{Score: score, Member: run.ID})
	}

	_, err = pipe.Exec(ctx)
	return err
}

// AppendEvent pushes one new RunEvent onto runID's event list (see
// RunStore's doc comment for why this is separate from SaveRun). A
// Redis list, via RPUSH, is the natural fit: Events is append-only and
// always read in the order it was written, exactly what a list gives
// for O(1) per append instead of rewriting the whole history.
func (s *RedisRunStore) AppendEvent(ctx context.Context, runID string, event RunEvent) error {
	data, err := json.Marshal(event)
	if err != nil {
		return err
	}
	return s.client.RPush(ctx, runEventsKey(runID), data).Err()
}

// AppendCompletedStep pushes one new step ID onto runID's completed
// list, the same RPUSH-based pattern as AppendEvent, for the same
// reason (see RunStore's doc comment).
func (s *RedisRunStore) AppendCompletedStep(ctx context.Context, runID, stepID string) error {
	return s.client.RPush(ctx, runCompletedStepsKey(runID), stepID).Err()
}

// AppendSkippedSteps pushes one or more new step IDs onto runID's
// skipped list in one call, since a conditional's losing branch can
// skip several steps at once (the branch itself plus every step
// transitively depending on it).
func (s *RedisRunStore) AppendSkippedSteps(ctx context.Context, runID string, stepIDs []string) error {
	if len(stepIDs) == 0 {
		return nil
	}
	args := make([]interface{}, len(stepIDs))
	for i, id := range stepIDs {
		args[i] = id
	}
	return s.client.RPush(ctx, runSkippedStepsKey(runID), args...).Err()
}

func (s *RedisRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	pipe := s.client.Pipeline()
	blobCmd := pipe.Get(ctx, runKey(runID))
	stepsCmd := pipe.Get(ctx, runStepsKey(runID))
	eventsCmd := pipe.LRange(ctx, runEventsKey(runID), 0, -1)
	completedCmd := pipe.LRange(ctx, runCompletedStepsKey(runID), 0, -1)
	skippedCmd := pipe.LRange(ctx, runSkippedStepsKey(runID), 0, -1)
	if _, err := pipe.Exec(ctx); err != nil && err != redis.Nil {
		return nil, err
	}

	data, err := blobCmd.Bytes()
	if err == redis.Nil {
		return nil, nil // run not found
	}
	if err != nil {
		return nil, err
	}

	var run Run
	if err := json.Unmarshal(data, &run); err != nil {
		return nil, err
	}

	if stepsData, err := stepsCmd.Bytes(); err == nil {
		if err := json.Unmarshal(stepsData, &run.Steps); err != nil {
			return nil, err
		}
	} else if err != redis.Nil {
		return nil, err
	}

	eventsData, err := eventsCmd.Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	run.Events = make([]RunEvent, 0, len(eventsData))
	for _, raw := range eventsData {
		var event RunEvent
		if err := json.Unmarshal([]byte(raw), &event); err != nil {
			return nil, err
		}
		run.Events = append(run.Events, event)
	}

	run.CompletedSteps, err = completedCmd.Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}
	run.SkippedSteps, err = skippedCmd.Result()
	if err != nil && err != redis.Nil {
		return nil, err
	}

	return &run, nil
}

// ListRuns picks the narrowest available index for opts (agent+status
// filtered runs use ZINTERSTORE against a temporary key so both
// filters apply together, agent-only or status-only use their
// dedicated set directly, no filter falls back to runs:by_time), then
// paginates it newest-first via ZREVRANGE.
func (s *RedisRunStore) ListRuns(ctx context.Context, opts ListRunsOptions) ([]*Run, error) {
	limit := opts.Limit
	if limit <= 0 || limit > maxListRunsLimit {
		limit = defaultListRunsLimit
	}

	key := runsByTimeKey()
	switch {
	case opts.AgentName != "" && opts.Status != "":
		// A key shared across every caller filtering on the same
		// agent+status would let two concurrent requests overwrite or
		// delete each other's ZINTERSTORE result mid-read - suffixing
		// with a fresh UUID per call keeps this request's temporary
		// key unique regardless of what anyone else is doing
		// concurrently. A short TTL is a backstop in case the deferred
		// Del below never runs (e.g. the process is killed), not the
		// actual isolation mechanism.
		key = "runs:tmp:by_agent_status:" + uuid.NewString()
		pipe := s.client.TxPipeline()
		pipe.ZInterStore(ctx, key, &redis.ZStore{
			Keys: []string{runsByAgentKey(opts.AgentName), runsByStatusKey(opts.Status)},
		})
		pipe.Expire(ctx, key, 30*time.Second)
		if _, err := pipe.Exec(ctx); err != nil {
			return nil, err
		}
		defer s.client.Del(ctx, key)
	case opts.AgentName != "":
		key = runsByAgentKey(opts.AgentName)
	case opts.Status != "":
		key = runsByStatusKey(opts.Status)
	}

	start := int64(opts.Offset)
	if start < 0 {
		start = 0 // a client-supplied negative offset would otherwise count from the end in ZRANGE instead of clamping to the first page
	}
	stop := start + int64(limit) - 1
	ids, err := s.client.ZRangeArgs(ctx, redis.ZRangeArgs{
		Key: key, Start: start, Stop: stop, Rev: true,
	}).Result()
	if err != nil {
		return nil, err
	}

	runs := make([]*Run, 0, len(ids))
	for _, id := range ids {
		run, err := s.GetRun(ctx, id)
		if err != nil {
			return nil, err
		}
		if run == nil {
			continue // index and the run's own key raced (e.g. an expiry); skip rather than fail the whole list
		}
		runs = append(runs, run)
	}
	return runs, nil
}

func runKey(runID string) string {
	return fmt.Sprintf("run:%s", runID)
}

func runStepsKey(runID string) string {
	return fmt.Sprintf("run:%s:steps", runID)
}

func runEventsKey(runID string) string {
	return fmt.Sprintf("run:%s:events", runID)
}

func runCompletedStepsKey(runID string) string {
	return fmt.Sprintf("run:%s:completed_steps", runID)
}

func runSkippedStepsKey(runID string) string {
	return fmt.Sprintf("run:%s:skipped_steps", runID)
}

func runsByTimeKey() string {
	return "runs:by_time"
}

func runsByAgentKey(agentName string) string {
	return fmt.Sprintf("runs:by_agent:%s", agentName)
}

func runsByStatusKey(status string) string {
	return fmt.Sprintf("runs:by_status:%s", status)
}
