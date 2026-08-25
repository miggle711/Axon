package engine

import "testing"

func TestValidateAgentDefinition_Valid(t *testing.T) {
	def := AgentDefinition{
		Name: "valid_agent",
		Steps: []StepDefinition{
			{ID: "step_1", Type: StepTypeToolCall, InputTemplate: "{{user_input}}", DependsOn: []string{}},
			{ID: "step_2", Type: StepTypeConditional, Condition: "{{step_1.output}} == success", OnTrue: "step_3", OnFalse: "", DependsOn: []string{"step_1"}},
			{ID: "step_3", Type: StepTypeToolCall, InputTemplate: "{{step_1.output}} and {{step_2.output}}", DependsOn: []string{"step_2"}},
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
			{ID: "step_1", Type: StepTypeToolCall, InputTemplate: "{{user_input}}", DependsOn: []string{}},
		}}
		if err := validateAgentDefinition(def); err != nil {
			t.Errorf("expected {{user_input}} to always be valid, got: %v", err)
		}
	})
}
