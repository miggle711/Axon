package main

import (
	"context"
	"testing"

	worker "axon-worker"
)

func TestRunStep(t *testing.T) {
	ctx := context.Background()
	payload := worker.StepPayload{Input: "hello"}

	toolOutput, err := runStep(ctx, worker.JobTypeToolCall, payload)
	if err != nil {
		t.Fatalf("tool_call: unexpected error: %v", err)
	}
	if toolOutput != "hello" {
		t.Errorf("tool_call: got %q, want %q", toolOutput, "hello")
	}

	llmOutput, err := runStep(ctx, worker.JobTypeLLMCall, payload)
	if err != nil {
		t.Fatalf("llm_call: unexpected error: %v", err)
	}
	if llmOutput == payload.Input {
		t.Errorf("llm_call: expected a distinguishable stub output, got the raw input back: %q", llmOutput)
	}

	if _, err := runStep(ctx, "unknown_type", payload); err == nil {
		t.Error("unknown job type: expected an error, got none")
	}
}
