package cli

import (
	"fmt"
	"strings"

	engine "axon-engine"
)

const previewLength = 200

// FormatRunStatus renders run as a human-readable status report: an
// overall status line, each step's state and a preview of its output
// in declaration order, and the final result if the run has completed.
func FormatRunStatus(run *engine.Run) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Run:    %s\n", run.ID)
	fmt.Fprintf(&b, "Agent:  %s\n", run.AgentName)
	fmt.Fprintf(&b, "Status: %s\n", run.Status)
	b.WriteString("\n")

	completed := toSet(run.CompletedSteps)
	skipped := toSet(run.SkippedSteps)

	b.WriteString("Steps:\n")
	for _, step := range run.Steps {
		state := "pending"
		switch {
		case skipped[step.ID]:
			state = "skipped"
		case completed[step.ID]:
			state = "completed"
		case run.StepResults[step.ID] != "":
			// A supervisor's Options step can produce output on each
			// loop iteration without ever landing in CompletedSteps
			// (see engine.OnStepCompleted) - reflect that as running
			// rather than silently showing "pending".
			state = "running"
		}

		fmt.Fprintf(&b, "  [%s] %s (%s)\n", stateGlyph(state), step.ID, state)
		if output, ok := run.StepResults[step.ID]; ok && output != "" {
			fmt.Fprintf(&b, "        %s\n", truncate(output, previewLength))
		}
	}

	if run.Status == "completed" && len(run.Steps) > 0 {
		lastStep := run.Steps[len(run.Steps)-1]
		if result, ok := run.StepResults[lastStep.ID]; ok {
			b.WriteString("\nResult:\n")
			b.WriteString(result)
			b.WriteString("\n")
		}
	}

	return b.String()
}

func stateGlyph(state string) string {
	switch state {
	case "completed":
		return "x"
	case "skipped":
		return "-"
	case "running":
		return "~"
	default:
		return " "
	}
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func toSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
