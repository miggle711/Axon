package cli

import (
	"strings"
	"testing"

	engine "axon-engine"
)

func TestFormatRunStatus_Completed(t *testing.T) {
	run := &engine.Run{
		ID:        "run-1",
		AgentName: "greeter",
		Status:    "completed",
		Steps: []engine.StepDefinition{
			{ID: "greet", Type: engine.StepTypeToolCall},
		},
		StepResults:    map[string]string{"greet": "Hello, world!"},
		CompletedSteps: []string{"greet"},
	}

	output := FormatRunStatus(run)

	for _, want := range []string{"run-1", "greeter", "completed", "greet", "Hello, world!"} {
		if !strings.Contains(output, want) {
			t.Errorf("expected output to contain %q, got:\n%s", want, output)
		}
	}
	// The final result should appear once as the step preview and once
	// under "Result:".
	if strings.Count(output, "Hello, world!") != 2 {
		t.Errorf("expected 'Hello, world!' to appear twice (step preview + final result), got %d times:\n%s",
			strings.Count(output, "Hello, world!"), output)
	}
}

func TestFormatRunStatus_SkippedStep(t *testing.T) {
	run := &engine.Run{
		ID:        "run-2",
		AgentName: "conditional_agent",
		Status:    "completed",
		Steps: []engine.StepDefinition{
			{ID: "check", Type: engine.StepTypeConditional},
			{ID: "on_false", Type: engine.StepTypeToolCall},
		},
		StepResults:    map[string]string{"check": ""},
		CompletedSteps: []string{"check"},
		SkippedSteps:   []string{"on_false"},
	}

	output := FormatRunStatus(run)
	if !strings.Contains(output, "on_false (skipped)") {
		t.Errorf("expected on_false to be marked skipped, got:\n%s", output)
	}
}

func TestFormatRunStatus_FailedStep(t *testing.T) {
	run := &engine.Run{
		ID:        "run-5",
		AgentName: "failing_agent",
		Status:    "failed",
		Steps: []engine.StepDefinition{
			{ID: "bad_step", Type: engine.StepTypeToolCall},
		},
		FailedSteps: []string{"bad_step"},
	}

	output := FormatRunStatus(run)
	if !strings.Contains(output, "Status: failed") {
		t.Errorf("expected overall Status to show 'failed', got:\n%s", output)
	}
	if !strings.Contains(output, "bad_step (failed)") {
		t.Errorf("expected bad_step to be marked failed, got:\n%s", output)
	}
}

func TestFormatRunStatus_InProgressNoResult(t *testing.T) {
	run := &engine.Run{
		ID:        "run-3",
		AgentName: "research_agent",
		Status:    "in_progress",
		Steps: []engine.StepDefinition{
			{ID: "search", Type: engine.StepTypeToolCall},
			{ID: "answer", Type: engine.StepTypeLLMCall},
		},
		StepResults:    map[string]string{"search": "some results"},
		CompletedSteps: []string{"search"},
	}

	output := FormatRunStatus(run)
	if strings.Contains(output, "Result:") {
		t.Errorf("expected no Result section for an in_progress run, got:\n%s", output)
	}
	if !strings.Contains(output, "answer (pending)") {
		t.Errorf("expected answer to be marked pending, got:\n%s", output)
	}
}

func TestFormatRunStatus_TruncatesLongOutput(t *testing.T) {
	longOutput := strings.Repeat("x", previewLength+50)
	run := &engine.Run{
		ID:        "run-4",
		AgentName: "agent",
		Status:    "in_progress",
		Steps: []engine.StepDefinition{
			{ID: "step1", Type: engine.StepTypeToolCall},
		},
		StepResults: map[string]string{"step1": longOutput},
	}

	output := FormatRunStatus(run)
	if strings.Contains(output, longOutput) {
		t.Error("expected long output to be truncated in the step preview")
	}
	if !strings.Contains(output, "...") {
		t.Error("expected truncated output to end with '...'")
	}
}
