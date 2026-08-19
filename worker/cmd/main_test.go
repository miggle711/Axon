package main

import (
	"context"
	"errors"
	"testing"

	worker "axon-worker"
)

func TestRunToolCall(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "hello"}

	output, err := runToolCall(ctx, payload)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if output != "hello" {
		t.Errorf("got %q, want %q", output, "hello")
	}
}

type fakeLLMClient struct {
	output string
	err    error
}

func (f *fakeLLMClient) Complete(ctx context.Context, prompt string) (string, error) {
	return f.output, f.err
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
