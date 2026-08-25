package engine

import (
	"fmt"
	"regexp"
)

// stepOutputPlaceholder matches {{step_id.output}} references in a
// template string, capturing step_id. Mirrors the exact format
// resolveTemplate substitutes (fmt.Sprintf("{{%s.output}}", stepID)), 
// no whitespace tolerance, since none is supported at resolve time either.
var stepOutputPlaceholder = regexp.MustCompile(`\{\{([^}]+)\.output\}\}`)

// validateAgentDefinition checks that def's steps form a well-formed
// DAG before any run is created from it: no duplicate step IDs, every
// step-ID reference (DependsOn, OnTrue, OnFalse, Options) points at a
// real step, no dependency cycles, and every {{step_id.output}}
// template placeholder references a real step. 
// Catches mistakes that would otherwise surface as a silent hang (a cycle: no step ever
// becomes enqueable) or silently-unresolved {{...}} text in a step's
// input, rather than a clear error, a real risk now that agents can
// be hand-authored JSON files (#7) rather than only constructed
// directly in Go.
func validateAgentDefinition(def AgentDefinition) error {
	stepIDs := make(map[string]bool, len(def.Steps))
	for _, step := range def.Steps {
		if stepIDs[step.ID] {
			return fmt.Errorf("agent %q: duplicate step ID %q", def.Name, step.ID)
		}
		stepIDs[step.ID] = true
	}

	referencesStep := func(field, stepID, refersTo string) error {
		if refersTo == "" {
			return nil // empty is valid: e.g. a conditional branch that intentionally does nothing
		}
		if !stepIDs[refersTo] {
			return fmt.Errorf("agent %q: step %q's %s references unknown step %q", def.Name, stepID, field, refersTo)
		}
		return nil
	}

	for _, step := range def.Steps {
		for _, dep := range step.DependsOn {
			if err := referencesStep("depends_on", step.ID, dep); err != nil {
				return err
			}
		}
		if err := referencesStep("on_true", step.ID, step.OnTrue); err != nil {
			return err
		}
		if err := referencesStep("on_false", step.ID, step.OnFalse); err != nil {
			return err
		}
		for _, opt := range step.Options {
			if err := referencesStep("options", step.ID, opt); err != nil {
				return err
			}
		}
	}

	if cyclePath, ok := findDependencyCycle(def.Steps); ok {
		return fmt.Errorf("agent %q: dependency cycle detected: %s", def.Name, cyclePath)
	}

	for _, step := range def.Steps {
		for _, template := range []struct {
			field, value string
		}{
			{"input_template", step.InputTemplate},
			{"prompt_template", step.PromptTemplate},
			{"condition", step.Condition},
		} {
			for _, match := range stepOutputPlaceholder.FindAllStringSubmatch(template.value, -1) {
				refID := match[1]
				if !stepIDs[refID] {
					return fmt.Errorf("agent %q: step %q's %s references unknown step %q via {{%s.output}}",
						def.Name, step.ID, template.field, refID, refID)
				}
			}
		}
	}

	return nil
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
