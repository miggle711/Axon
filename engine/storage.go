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
}

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

func (s *RedisRunStore) SaveRun(ctx context.Context, run *Run) error {
	data, err := json.Marshal(run)
	if err != nil {
		return err
	}
	return s.client.Set(ctx, runKey(run.ID), data, 0).Err()
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

func runKey(runID string) string {
	return fmt.Sprintf("run:%s", runID)
}
