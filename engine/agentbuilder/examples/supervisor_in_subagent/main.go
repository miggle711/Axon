// Command supervisor_in_subagent generates two agents testing a
// combination nothing in this repo has exercised yet: a supervisor
// loop living inside a sub-agent, called via agent_call from a parent
// run. Every existing agent uses supervisor or agent_call, never both
// together, and never a supervisor nested one run deep.
//
// What this actually exercises: engine.spawnChildRun creates a fresh
// Run for the sub-agent with its own SupervisorIterations and
// ActiveSupervisorChoice maps, so the sub-agent's loop state should be
// fully isolated from the parent run's. Whether that isolation
// actually holds, and whether the parent's agent_call step correctly
// waits for the child run's supervisor loop to finish looping (not
// just for its first step to complete) before reading output_step,
// are both real, previously untested questions - not just a shape
// that happens to validate.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/supervisor_in_subagent
//
// which writes both files directly into agents/ (unlike
// html_to_markdown_safe's example, which prints to stdout) since this
// one only makes sense as a pair of files that reference each other by
// name, not as a single piece of output to redirect.
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
	fmt.Println("Wrote engine/agents/supervisor_subagent.json and engine/agents/supervisor_subagent_caller.json")
}

// writeSubAgent builds the sub-agent: a supervisor loop structurally
// identical to iterative_writer_agent.json's shape (editor forces its
// first move to draft via FirstIteration, then makes a real decision
// from the second iteration on), but as a standalone agent with
// output_step set, which iterative_writer_agent.json never needed
// since nothing calls it as a sub-agent.
func writeSubAgent() error {
	// draft's own prompt_template references "{{draft.output}}" as a
	// plain string, not via Output(draft): that would need draft's
	// own *Step to exist before it's constructed, which the builder
	// has no way to express. This is the one place a self-referencing
	// step still has to be written by hand, same as the real,
	// hand-written iterative_writer_agent.json this mirrors does.
	draft := ab.LLM("draft", "Write a short, clear paragraph answering the question below. If a previous draft is shown, improve on it rather than starting over.\n\nQuestion: {{user_input}}\n\nPrevious draft (if any):\n{{draft.output}}")
	editor := ab.Supervisor("editor",
		"You are editing a short paragraph answering a question, judging the latest draft below.\n\nQuestion: {{user_input}}\n\nLatest draft:\n"+ab.Output(draft)+"\n\nIf the draft answers the question clearly and concisely, respond with exactly: done\nOtherwise, respond with exactly: draft\n\nRespond with only one of those two words, nothing else.",
		[]*ab.Step{draft},
	).FirstIteration(draft)

	agent := ab.New("supervisor_subagent", editor, draft).
		WithDescription("Sub-agent half of the supervisor-inside-agent_call combination test. Structurally identical to iterative_writer_agent.json's editor/draft loop, but with output_step set so it can actually be called via agent_call - iterative_writer_agent.json never needed that since nothing calls it as a sub-agent. Requires GROQ_API_KEY on the worker.").
		WithOutputStep(editor)

	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/supervisor_subagent.json", data)
}

// writeParentAgent builds the caller: one agent_call step into the
// sub-agent above, then one more step using its result - the same
// shape as greeter_caller.json, just calling a supervisor-based
// sub-agent instead of a single-tool_call one.
func writeParentAgent() error {
	call := ab.AgentCall("call_subagent", "supervisor_subagent", "{{user_input}}")
	after := ab.Tool("after_call", "echo", "sub-agent's editor settled on: "+ab.Output(call), call)

	agent := ab.New("supervisor_subagent_caller", call, after).
		WithDescription("Parent half of the supervisor-inside-agent_call combination test: calls supervisor_subagent.json, then echoes its result. Proves a supervisor loop living inside a sub-agent actually works end to end, not just that the shape validates.").
		WithOutputStep(after)

	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/supervisor_subagent_caller.json", data)
}

// writeWithSchema inserts "$schema": "./agent.schema.json" as the
// first key, same as every other committed agent, then writes path -
// Agent.JSON() deliberately doesn't include $schema itself (see its
// doc comment), since the right relative path depends on where the
// file ends up.
func writeWithSchema(path string, data []byte) error {
	// data starts with "{\n" (json.MarshalIndent's opening brace and
	// newline); keep that, insert the $schema line right after it,
	// then the rest of data unchanged.
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	return os.WriteFile(path, withSchema, 0644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
