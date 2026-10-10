// Command fan_in_with_supervisor tests a fan-in where one of the
// parallel branches is a supervisor loop rather than a single
// tool_call or llm_call. multi_source_research_agent.json already
// proves fan-in works with two plain branches; this tests whether a
// combine step can wait correctly on a branch that itself loops for
// several iterations before settling, not just a branch that
// completes in one step.
//
// What this actually exercises: whether canEnqueueStep's dependency
// check for combine (which depends on both branch_a and the
// supervisor branch) correctly treats the supervisor as "done" only
// once it actually emits "done" and stops looping, not after its
// first iteration completes.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/fan_in_with_supervisor
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	branchA := ab.Tool("branch_a", "echo", "{{user_input}} (branch a)")

	draft := ab.LLM("draft", "Write a short, clear paragraph answering the question below. If a previous draft is shown, improve on it rather than starting over.\n\nQuestion: {{user_input}}\n\nPrevious draft (if any):\n{{draft.output}}")
	editor := ab.Supervisor("editor",
		"You are editing a short paragraph answering a question, judging the latest draft below.\n\nQuestion: {{user_input}}\n\nLatest draft:\n"+ab.Output(draft)+"\n\nIf the draft answers the question clearly and concisely, respond with exactly: done\nOtherwise, respond with exactly: draft\n\nRespond with only one of those two words, nothing else.",
		[]*ab.Step{draft},
	).FirstIteration(draft)

	combine := ab.LLM("combine",
		"Combine these two results.\n\nBranch A:\n"+ab.Output(branchA)+"\n\nSupervisor branch (editor's settled draft):\n"+ab.Output(editor),
		branchA, editor,
	)

	agent := ab.New("fan_in_with_supervisor", branchA, draft, editor, combine).
		WithDescription("Tests a fan-in where one parallel branch is a supervisor loop rather than a single step: branch_a is a plain tool_call, the other branch is editor's supervisor loop over draft, and combine waits on both. Confirms combine only runs once editor has actually settled (emitted done), not after editor's first iteration. Requires GROQ_API_KEY on the worker.").
		WithOutputStep(combine)

	data, err := agent.JSON()
	if err != nil {
		fail(err)
	}
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	if err := os.WriteFile("agents/fan_in_with_supervisor.json", withSchema, 0644); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/fan_in_with_supervisor.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
