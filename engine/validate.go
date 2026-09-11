package engine

import (
	"errors"
	"fmt"
	"regexp"
)

// stepOutputPlaceholder matches {{step_id.output}} references in a
// template string, capturing step_id. Mirrors the exact format
// resolveTemplate substitutes (fmt.Sprintf("{{%s.output}}", stepID)),
// no whitespace tolerance, since none is supported at resolve time either.
var stepOutputPlaceholder = regexp.MustCompile(`\{\{([^}]+)\.output\}\}`)

// stepIterationPlaceholder matches {{step_id.iteration}} references,
// the same way stepOutputPlaceholder matches {{step_id.output}}.
var stepIterationPlaceholder = regexp.MustCompile(`\{\{([^}]+)\.iteration\}\}`)

// validateAgentDefinition checks that def's steps form a well-formed
// DAG before any run is created from it: no duplicate step IDs, every
// step-ID reference (DependsOn, OnTrue, OnFalse, Options) points at a
// real step, a step's FirstIterationStep (if set) is one of its own
// Options, no dependency cycles, every {{step_id.output}} or
// {{step_id.iteration}} template placeholder references a real step,
// every step's Type is one of the five known step types, and each
// step has the field its type actually needs to run (a tool for
// tool_call, a prompt_template for llm_call/supervisor plus at least
// one option for supervisor, a condition for conditional, an agent for
// agent_call). Catches mistakes that would otherwise surface as a
// silent hang (a cycle: no step ever becomes enqueable), silently
// unresolved {{...}} text in a step's input, or a job the worker can
// only reject once it's already been dequeued (a typo'd type, or a
// required field missing), rather than a clear error - a real risk now
// that agents can be hand-authored JSON files (#7) rather than only
// constructed directly in Go.
//
// Duplicate step IDs and dependency cycles are checked first and
// returned immediately on their own: every other check assumes a
// unique, acyclic step set, so continuing past either would risk
// confusing cascade errors built on a broken foundation. Everything
// else (dangling references, first_iteration_step, dangling template
// placeholders, unknown/missing step type fields) is collected and
// returned together via errors.Join, so fixing a hand-authored agent
// doesn't mean one fix-and-rerun cycle per mistake (#54).
func validateAgentDefinition(def AgentDefinition) error {
	stepIDs := make(map[string]bool, len(def.Steps))
	for _, step := range def.Steps {
		if stepIDs[step.ID] {
			return fmt.Errorf("agent %q: duplicate step ID %q", def.Name, step.ID)
		}
		stepIDs[step.ID] = true
	}

	if cyclePath, ok := findDependencyCycle(def.Steps); ok {
		return fmt.Errorf("agent %q: dependency cycle detected: %s", def.Name, cyclePath)
	}

	var errs []error

	referencesStep := func(field, stepID, refersTo string) {
		if refersTo == "" {
			return // empty is valid: e.g. a conditional branch that intentionally does nothing
		}
		if !stepIDs[refersTo] {
			errs = append(errs, fmt.Errorf("agent %q: step %q's %s references unknown step %q", def.Name, stepID, field, refersTo))
		}
	}

	validStepTypes := map[StepType]bool{
		StepTypeToolCall:    true,
		StepTypeLLMCall:     true,
		StepTypeConditional: true,
		StepTypeAgentCall:   true,
		StepTypeSupervisor:  true,
	}

	for _, step := range def.Steps {
		for _, dep := range step.DependsOn {
			referencesStep("depends_on", step.ID, dep)
		}
		referencesStep("on_true", step.ID, step.OnTrue)
		referencesStep("on_false", step.ID, step.OnFalse)
		for _, opt := range step.Options {
			referencesStep("options", step.ID, opt)
		}
		if step.FirstIterationStep != "" {
			isOption := false
			for _, opt := range step.Options {
				if opt == step.FirstIterationStep {
					isOption = true
					break
				}
			}
			if !isOption {
				errs = append(errs, fmt.Errorf("agent %q: step %q's first_iteration_step %q must be one of its options %v",
					def.Name, step.ID, step.FirstIterationStep, step.Options))
			}
		}

		// A typo'd or unset type (e.g. "tool_cal") falls straight
		// through every step.Type == StepTypeX check in orchestrator.go
		// and gets enqueued as a generic job anyway, only failing once
		// the worker rejects the unrecognized job type - checking the
		// type itself here, and the required field each type actually
		// needs (#54), catches both at authoring time instead. Only
		// checked when step.Type is itself one of the five known
		// values, so an unrecognized type reports just the one error
		// instead of also complaining about "missing" fields that
		// wouldn't even apply to whatever the author actually meant.
		if !validStepTypes[step.Type] {
			errs = append(errs, fmt.Errorf("agent %q: step %q has unknown type %q (must be one of tool_call, llm_call, conditional, agent_call, supervisor)",
				def.Name, step.ID, step.Type))
			continue
		}

		switch step.Type {
		case StepTypeToolCall:
			if step.Tool == "" {
				errs = append(errs, fmt.Errorf("agent %q: step %q is a tool_call step but has no tool set", def.Name, step.ID))
			}
		case StepTypeLLMCall:
			if step.PromptTemplate == "" {
				errs = append(errs, fmt.Errorf("agent %q: step %q is an llm_call step but has no prompt_template set", def.Name, step.ID))
			}
		case StepTypeSupervisor:
			if step.PromptTemplate == "" {
				errs = append(errs, fmt.Errorf("agent %q: step %q is a supervisor step but has no prompt_template set", def.Name, step.ID))
			}
			if len(step.Options) == 0 {
				errs = append(errs, fmt.Errorf("agent %q: step %q is a supervisor step but has no options set", def.Name, step.ID))
			}
		case StepTypeConditional:
			if step.Condition == "" {
				errs = append(errs, fmt.Errorf("agent %q: step %q is a conditional step but has no condition set", def.Name, step.ID))
			}
		case StepTypeAgentCall:
			if step.Agent == "" {
				errs = append(errs, fmt.Errorf("agent %q: step %q is an agent_call step but has no agent set", def.Name, step.ID))
			}
		}
	}

	for _, step := range def.Steps {
		for _, template := range []struct {
			field, value string
		}{
			{"input_template", step.InputTemplate},
			{"prompt_template", step.PromptTemplate},
			{"condition", step.Condition},
		} {
			for _, placeholderKind := range []struct {
				suffix string
				re     *regexp.Regexp
			}{
				{"output", stepOutputPlaceholder},
				{"iteration", stepIterationPlaceholder},
			} {
				for _, match := range placeholderKind.re.FindAllStringSubmatch(template.value, -1) {
					refID := match[1]
					if !stepIDs[refID] {
						errs = append(errs, fmt.Errorf("agent %q: step %q's %s references unknown step %q via {{%s.%s}}",
							def.Name, step.ID, template.field, refID, refID, placeholderKind.suffix))
					}
				}
			}
		}
	}

	return errors.Join(errs...)
}

// findDependencyCycle runs a DFS with three-color marking over steps'
// DependsOn edges. A gray node revisited while still on the DFS stack
// is a back-edge, i.e. a cycle; unwinding the stack at that point gives
// the actual cycle path for a useful error message (e.g.
// "step_a -> step_b -> step_a").
func findDependencyCycle(steps []StepDefinition) (string, bool) {
	const (
		white = iota // unvisited
		gray         // on the current DFS stack
		black        // fully explored, no cycle through it
	)

	byID := make(map[string]StepDefinition, len(steps))
	for _, step := range steps {
		byID[step.ID] = step
	}

	color := make(map[string]int, len(steps))
	var stack []string

	var visit func(stepID string) (string, bool)
	visit = func(stepID string) (string, bool) {
		color[stepID] = gray
		stack = append(stack, stepID)

		for _, dep := range byID[stepID].DependsOn {
			switch color[dep] {
			case gray:
				// Found the back-edge; build the cycle path from where
				// dep first appears on the stack through to here.
				start := 0
				for i, id := range stack {
					if id == dep {
						start = i
						break
					}
				}
				cycle := append([]string{}, stack[start:]...)
				cycle = append(cycle, dep)
				return joinArrow(cycle), true
			case white:
				if path, found := visit(dep); found {
					return path, true
				}
			}
		}

		stack = stack[:len(stack)-1]
		color[stepID] = black
		return "", false
	}

	for _, step := range steps {
		if color[step.ID] == white {
			if path, found := visit(step.ID); found {
				return path, true
			}
		}
	}

	return "", false
}

func joinArrow(ids []string) string {
	result := ids[0]
	for _, id := range ids[1:] {
		result += " -> " + id
	}
	return result
}
