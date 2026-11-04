// Command llm_conditional_classifier builds a real example for #25's
// documented gap: nothing in this repo demonstrates the safe way to
// route a conditional on an llm_call's output. evaluateCondition only
// supports ==, !=, and contains (engine/orchestrator.go), all exact
// string comparisons - safe against a tool_call's predictable output,
// brittle against an LLM's free-form text, which might answer "Yes,
// it does." instead of "yes".
//
// The convention this demonstrates: constrain classify's prompt to
// respond with exactly one word, then route with contains rather than
// == (so trailing punctuation or case differences in what the model
// actually returns don't break the match outright) - the same
// discipline every real supervisor prompt in this repo already uses
// ("respond with exactly: done"), just applied to a plain llm_call
// feeding a conditional instead of a supervisor's own decision step.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/llm_conditional_classifier
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	classify := ab.LLM("classify",
		"Decide whether the question below can be answered from general knowledge, without looking anything up.\n\n"+
			"Question: {{user_input}}\n\n"+
			"Respond with exactly one word: yes or no. Nothing else.")

	answerDirectly := ab.LLM("answer_directly",
		"Answer the question directly and concisely.\n\nQuestion: {{user_input}}")
	needsLookup := ab.Tool("needs_lookup", "echo",
		"This question needs a lookup, not just general knowledge: {{user_input}}")

	route := ab.Conditional("route", ab.Output(classify)+" contains yes", answerDirectly, needsLookup, classify)

	agent := ab.New("llm_conditional_classifier", classify, route, answerDirectly, needsLookup).
		WithDescription("Demonstrates #25's safe convention for routing a conditional on an llm_call's output: classify is constrained to answer exactly \"yes\" or \"no\", and route matches with contains rather than ==, so minor variation in what the model actually returns (trailing punctuation, capitalization) doesn't break the match the way == would. No output_step is set here since only one branch actually runs per run, same reasoning as the conditional scaffold (axon init --type conditional). Requires GROQ_API_KEY on the worker.")

	data, err := agent.JSON()
	if err != nil {
		fail(err)
	}
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	if err := os.WriteFile("agents/llm_conditional_classifier.json", withSchema, 0644); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/llm_conditional_classifier.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
