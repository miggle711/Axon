package main

import (
	"context"
	"errors"
	"testing"

	worker "axon-worker"
	"axon-worker/tools"
)

func TestToolCallRunner(t *testing.T) {
	ctx := context.Background()
	registry := map[string]tools.Tool{"echo": tools.Echo{}}
	runner := newToolCallRunner(registry)

	t.Run("dispatches to the named tool", func(t *testing.T) {
		output, err := runner(ctx, worker.StepPayload{Tool: "echo", Input: "hello"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if output != "hello" {
			t.Errorf("got %q, want %q", output, "hello")
		}
	})

	t.Run("unknown tool errors instead of falling back", func(t *testing.T) {
		if _, err := runner(ctx, worker.StepPayload{Tool: "does_not_exist", Input: "hello"}); err == nil {
			t.Error("expected an error for an unregistered tool, got none")
		}
	})
}

type fakeLLMClient struct {
	output    string
	err       error
	decision  string
	decideErr error
}

func (f *fakeLLMClient) Complete(ctx context.Context, prompt string) (string, error) {
	return f.output, f.err
}

func (f *fakeLLMClient) Decide(ctx context.Context, prompt string, options []string) (string, error) {
	return f.decision, f.decideErr
}

func TestLLMRunner(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "prompt"}

	runner := newLLMRunner(&fakeLLMClient{output: "a real completion"})
	output, err := runner(ctx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "a real completion" {
		t.Errorf("got %q, want %q", output, "a real completion")
	}

	failingRunner := newLLMRunner(&fakeLLMClient{err: errors.New("boom")})
	if _, err := failingRunner(ctx, payload); err == nil {
		t.Error("expected an error when the client fails, got none")
	}
}

func TestSupervisorRunner(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "prompt", Options: []string{"search"}}

	runner := newSupervisorRunner(&fakeLLMClient{decision: "search"})
	output, err := runner(ctx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "search" {
		t.Errorf("got %q, want %q", output, "search")
	}

	failingRunner := newSupervisorRunner(&fakeLLMClient{decideErr: errors.New("boom")})
	if _, err := failingRunner(ctx, payload); err == nil {
		t.Error("expected an error when the client fails, got none")
	}
}
