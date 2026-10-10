// Command supervisor_two_options tests a supervisor with two genuinely
// distinct options rather than one. Every existing supervisor in this
// repo (research_agent.json, iterative_writer_agent.json) loops on a
// single option; nothing tests whether the LLM's decision correctly
// dispatches between two different step IDs when both are valid
// choices, and whether first_iteration_step (forcing the very first
// move) still behaves correctly when there's more than one option to
// eventually choose between on later iterations.
//
// What this actually exercises: the decision-matching loop in
// engine/orchestrator.go (processSupervisorDecision) against
// supervisorStep.Options with len > 1, and whether
// run.ActiveSupervisorChoice correctly records whichever of the two
// options was actually picked, not just whichever was picked first.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/supervisor_two_options
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	search := ab.Tool("search", "echo", "search result for: {{user_input}}")
	calculate := ab.Tool("calculate", "echo", "calculation result for: {{user_input}}")

	judge := ab.Supervisor("judge",
		"You are deciding what to do next to answer the question below.\n\n"+
			"Question: {{user_input}}\n\n"+
			"Most recent search result:\n"+ab.Output(search)+"\n\n"+
			"Most recent calculation result:\n"+ab.Output(calculate)+"\n\n"+
			"If you need to look something up, respond with exactly: search\n"+
			"If you need to compute something, respond with exactly: calculate\n"+
			"If you have enough to answer the question, respond with exactly: done\n\n"+
			"Respond with only one of those three words, nothing else.",
		[]*ab.Step{search, calculate},
	).FirstIteration(search)

	agent := ab.New("supervisor_two_options", judge, search, calculate).
		WithDescription("Tests a supervisor choosing between two genuinely distinct options (search, calculate) rather than looping on a single one. judge forces its first move to search via first_iteration_step, then makes a real LLM decision among three outcomes (search, calculate, done) on later iterations. Confirms the decision-matching logic in processSupervisorDecision correctly dispatches to whichever of several options the LLM actually names. Requires GROQ_API_KEY on the worker.").
		WithOutputStep(judge)

	data, err := agent.JSON()
	if err != nil {
		fail(err)
	}
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	if err := os.WriteFile("agents/supervisor_two_options.json", withSchema, 0644); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/supervisor_two_options.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
