package engine

import (
	"strings"
	"testing"
)

func TestValidateAgentDefinition_Valid(t *testing.T) {
	def := AgentDefinition{
		Name: "valid_agent",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "{{user_input}}", DependsOn: []string{}},
			{ID: "step_2", Type: StepTypeConditional, Condition: "{{step_1.output}} == success", OnTrue: "step_3", OnFalse: "", DependsOn: []string{"step_1"}},
			{ID: "step_3", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "{{step_1.output}} and {{step_2.output}}", DependsOn: []string{"step_2"}},
		},
	}
	if err := validateAgentDefinition(def); err != nil {
		t.Errorf("expected a well-formed agent to validate cleanly, got: %v", err)
	}
}

func TestValidateAgentDefinition_DuplicateStepID(t *testing.T) {
	def := AgentDefinition{
		Name: "dup",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{}},
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{}},
		},
	}
	if err := validateAgentDefinition(def); err == nil {
		t.Fatal("expected an error for duplicate step IDs, got none")
	}
}

func TestValidateAgentDefinition_DanglingDependsOn(t *testing.T) {
	def := AgentDefinition{
		Name: "dangling_dep",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{"does_not_exist"}},
		},
	}
	if err := validateAgentDefinition(def); err == nil {
		t.Fatal("expected an error for a dangling depends_on reference, got none")
	}
}

func TestValidateAgentDefinition_DanglingOnTrueOnFalse(t *testing.T) {
	t.Run("on_true", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "cond", Type: StepTypeConditional, Condition: "{{user_input}} == x", OnTrue: "missing", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling on_true reference, got none")
		}
	})

	t.Run("on_false", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "cond", Type: StepTypeConditional, Condition: "{{user_input}} == x", OnFalse: "missing", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling on_false reference, got none")
		}
	})

	t.Run("empty on_true/on_false is valid", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "cond", Type: StepTypeConditional, Condition: "{{user_input}} == x", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected empty on_true/on_false to be valid (intentional no-op branch), got: %v", err)
		}
	})
}

func TestValidateAgentDefinition_DanglingOption(t *testing.T) {
	def := AgentDefinition{
		Name: "a",
		Steps: []StepDefinition{
			{ID: "supervisor_step", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{"missing_option"}, DependsOn: []string{}},
		},
	}
	if err := validateAgentDefinition(def); err == nil {
		t.Fatal("expected an error for a dangling options reference, got none")
	}
}

func TestValidateAgentDefinition_FirstIterationStep(t *testing.T) {
	t.Run("must be one of options", func(t *testing.T) {
		def := AgentDefinition{
			Name: "a",
			Steps: []StepDefinition{
				{ID: "supervisor_step", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{"option_a"}, FirstIterationStep: "option_b", DependsOn: []string{}},
				{ID: "option_a", Type: StepTypeToolCall, DependsOn: []string{"supervisor_step"}},
			},
		}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a first_iteration_step not in options, got none")
		}
	})

	t.Run("a value matching one of options is valid", func(t *testing.T) {
		def := AgentDefinition{
			Name: "a",
			Steps: []StepDefinition{
				{ID: "supervisor_step", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{"option_a"}, FirstIterationStep: "option_a", DependsOn: []string{}},
				{ID: "option_a", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{"supervisor_step"}},
			},
		}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected first_iteration_step matching an option to be valid, got: %v", err)
		}
	})

	t.Run("empty first_iteration_step is valid", func(t *testing.T) {
		def := AgentDefinition{
			Name: "a",
			Steps: []StepDefinition{
				{ID: "supervisor_step", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{"option_a"}, DependsOn: []string{}},
				{ID: "option_a", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{"supervisor_step"}},
			},
		}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected empty first_iteration_step to be valid (optional field), got: %v", err)
		}
	})
}

func TestValidateAgentDefinition_Cycle(t *testing.T) {
	t.Run("direct cycle", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{"step_2"}},
			{ID: "step_2", Type: StepTypeToolCall, DependsOn: []string{"step_1"}},
		}}
		err := validateAgentDefinition(def)
		if err == nil {
			t.Fatal("expected an error for a direct cycle, got none")
		}
	})

	t.Run("longer cycle", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_a", Type: StepTypeToolCall, DependsOn: []string{"step_c"}},
			{ID: "step_b", Type: StepTypeToolCall, DependsOn: []string{"step_a"}},
			{ID: "step_c", Type: StepTypeToolCall, DependsOn: []string{"step_b"}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a longer cycle, got none")
		}
	})

	t.Run("self-dependency", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{"step_1"}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a step depending on itself, got none")
		}
	})
}

func TestValidateAgentDefinition_DanglingTemplatePlaceholder(t *testing.T) {
	t.Run("input_template", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, InputTemplate: "{{missing_step.output}}", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling {{...}} placeholder in input_template, got none")
		}
	})

	t.Run("prompt_template", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeLLMCall, PromptTemplate: "summarize {{missing_step.output}}", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling {{...}} placeholder in prompt_template, got none")
		}
	})

	t.Run("condition", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeConditional, Condition: "{{missing_step.output}} == x", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling {{...}} placeholder in condition, got none")
		}
	})

	t.Run("{{user_input}} is always valid", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "{{user_input}}", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected {{user_input}} to always be valid, got: %v", err)
		}
	})

	t.Run("dangling {{...}} .iteration placeholder", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "judge", Type: StepTypeSupervisor, PromptTemplate: "iteration {{missing_step.iteration}}", Options: []string{}, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a dangling {{...}} .iteration placeholder, got none")
		}
	})

	t.Run(".iteration referencing a real, non-supervisor step is valid", func(t *testing.T) {
		// validateAgentDefinition only checks the referenced step
		// exists, not its type - matching .output's existing leniency.
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, Tool: "echo", InputTemplate: "{{step_1.iteration}}", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected .iteration on a real step ID to be valid regardless of step type, got: %v", err)
		}
	})
}

func TestValidateAgentDefinition_UnknownStepType(t *testing.T) {
	def := AgentDefinition{Name: "a", Steps: []StepDefinition{
		{ID: "step_1", Type: "tool_cal", Tool: "echo", DependsOn: []string{}}, // typo'd type
	}}
	err := validateAgentDefinition(def)
	if err == nil {
		t.Fatal("expected an error for an unrecognized step type, got none")
	}
	if !strings.Contains(err.Error(), "tool_cal") {
		t.Errorf("expected the error to mention the bad type value, got: %v", err)
	}
}

// TestValidateAgentDefinition_RequiredFieldsPerStepType covers #54: a
// step missing the one field its type actually needs to run used to
// only fail once the worker rejected the resulting job at runtime
// (empty tool name, empty prompt, etc), not at authoring time.
func TestValidateAgentDefinition_RequiredFieldsPerStepType(t *testing.T) {
	t.Run("tool_call needs tool", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a tool_call step with no tool, got none")
		}
	})

	t.Run("llm_call needs prompt_template", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeLLMCall, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for an llm_call step with no prompt_template, got none")
		}
	})

	t.Run("supervisor needs prompt_template", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeSupervisor, Options: []string{"opt"}, DependsOn: []string{}},
			{ID: "opt", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{"step_1"}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a supervisor step with no prompt_template, got none")
		}
	})

	t.Run("supervisor needs at least one option", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{}, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a supervisor step with no options, got none")
		}
	})

	t.Run("conditional needs condition", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeConditional, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for a conditional step with no condition, got none")
		}
	})

	t.Run("agent_call needs agent", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeAgentCall, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err == nil {
			t.Fatal("expected an error for an agent_call step with no agent, got none")
		}
	})

	t.Run("a fully specified step of each type is valid", func(t *testing.T) {
		def := AgentDefinition{Name: "a", Steps: []StepDefinition{
			{ID: "tool_step", Type: StepTypeToolCall, Tool: "echo", DependsOn: []string{}},
			{ID: "llm_step", Type: StepTypeLLMCall, PromptTemplate: "hi", DependsOn: []string{}},
			{ID: "cond_step", Type: StepTypeConditional, Condition: "{{user_input}} == x", DependsOn: []string{}},
			{ID: "supervisor_step", Type: StepTypeSupervisor, PromptTemplate: "decide", Options: []string{"tool_step"}, DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected a fully specified agent to validate cleanly, got: %v", err)
		}
	})
}

// TestValidateAgentDefinition_CollectsAllErrors covers #54: fixing a
// hand-authored agent one mistake at a time, rerunning validation after
// each fix, is exactly the friction this is meant to remove. A def with
// several independent mistakes should report all of them from a single
// call, not just the first one found.
func TestValidateAgentDefinition_CollectsAllErrors(t *testing.T) {
	def := AgentDefinition{
		Name: "a",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, DependsOn: []string{"missing_dep"}},
			{ID: "step_2", Type: StepTypeConditional, Condition: "{{user_input}} == x", OnTrue: "missing_on_true", DependsOn: []string{}},
			{ID: "step_3", Type: StepTypeToolCall, InputTemplate: "{{missing_placeholder.output}}", DependsOn: []string{}},
		},
	}

	err := validateAgentDefinition(def)
	if err == nil {
		t.Fatal("expected errors for a def with 3 independent mistakes, got none")
	}

	msg := err.Error()
	for _, want := range []string{"missing_dep", "missing_on_true", "missing_placeholder"} {
		if !strings.Contains(msg, want) {
			t.Errorf("expected the combined error to mention %q, got: %s", want, msg)
		}
	}
}
