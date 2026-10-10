// Command output_step_conditional tests #56's fourth constraint for
// real: a sub-agent whose output_step names a conditional step. Traced
// from the code first (engine/orchestrator.go's conditional branch
// sets run.StepResults[step.ID] to the winning branch's step ID
// string, not that branch's actual output), this generates the real
// agent pair to confirm live whether a parent calling it actually
// gets back the branch's real output, or just the literal step ID
// string like "on_true_step".
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/output_step_conditional
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	if err := writeSubAgent(); err != nil {
		fail(err)
	}
	if err := writeParentAgent(); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/conditional_subagent.json and engine/agents/conditional_subagent_caller.json")
}

// writeSubAgent builds a sub-agent whose output_step names the
// conditional itself, not one of its branches - the untested shape.
func writeSubAgent() error {
	step1 := ab.Tool("step_1", "echo", "success")
	onTrue := ab.Tool("on_true_step", "echo", "this is the real on_true_step output")
	onFalse := ab.Tool("on_false_step", "echo", "this is the real on_false_step output")
	check := ab.Conditional("check", ab.Output(step1)+" == success", onTrue, onFalse, step1)

	agent := ab.New("conditional_subagent", step1, check, onTrue, onFalse).
		WithDescription("Sub-agent for #56's output_step-under-branching test: output_step names check (the conditional) itself, not one of its branches. step_1 always outputs \"success\", so check always routes to on_true_step - confirming live whether a parent calling this gets back on_true_step's real output, or the literal string \"on_true_step\" (check's own StepResults entry, per engine/orchestrator.go, is the winning branch's step ID, not its output).").
		WithOutputStep(check)
	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/conditional_subagent.json", data)
}

// writeParentAgent calls the sub-agent above and echoes whatever it
// got back, so the live result is visible directly in the run.
func writeParentAgent() error {
	call := ab.AgentCall("call_subagent", "conditional_subagent", "{{user_input}}")
	after := ab.Tool("after_call", "echo", "sub-agent returned: "+ab.Output(call), call)

	agent := ab.New("conditional_subagent_caller", call, after).
		WithDescription("Parent half of #56's output_step-under-branching test: calls conditional_subagent.json and echoes exactly what it got back.").
		WithOutputStep(after)
	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/conditional_subagent_caller.json", data)
}

func writeWithSchema(path string, data []byte) error {
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	return os.WriteFile(path, withSchema, 0644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
