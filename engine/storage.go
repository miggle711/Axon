package engine

import (
	"context"
	"encoding/json"
	"fmt"

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
func (s *RedisRunStore) SaveRun(ctx context.Context, run *Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}

	pipe := s.client.TxPipeline()
	pipe.Set(ctx, runKey(run.ID), data, 0)

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

func (s *RedisRunStore) GetRun(ctx context.Context, runID string) (*Run, error) {
	data, err := s.client.Get(ctx, runKey(runID)).Bytes()
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
		key = fmt.Sprintf("runs:by_agent_status:%s:%s", opts.AgentName, opts.Status)
		if err := s.client.ZInterStore(ctx, key, &redis.ZStore{
			Keys: []string{runsByAgentKey(opts.AgentName), runsByStatusKey(opts.Status)},
		}).Err(); err != nil {
			return nil, err
		}
		defer s.client.Del(ctx, key)
	case opts.AgentName != "":
		key = runsByAgentKey(opts.AgentName)
	case opts.Status != "":
		key = runsByStatusKey(opts.Status)
	}

	start := int64(opts.Offset)
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

func runsByTimeKey() string {
	return "runs:by_time"
}

func runsByAgentKey(agentName string) string {
	return fmt.Sprintf("runs:by_agent:%s", agentName)
}

func runsByStatusKey(status string) string {
	return fmt.Sprintf("runs:by_status:%s", status)
}
