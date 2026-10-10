// Command conditional_into_agent_call tests a combination nothing in
// this repo has exercised yet: a conditional step whose on_true branch
// is an agent_call, spawning a real child run as the result of a
// branching decision, rather than a plain tool_call.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/conditional_into_agent_call
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	step1 := ab.Tool("step_1", "echo", "success")
	callGreeter := ab.AgentCall("call_greeter", "greeter", "{{user_input}}")
	onFalse := ab.Tool("on_false_step", "echo", "took the false branch")
	check := ab.Conditional("check", ab.Output(step1)+" == success", callGreeter, onFalse, step1)

	agent := ab.New("conditional_into_agent_call", step1, check, callGreeter, onFalse).
		WithDescription("Tests a conditional step whose on_true branch is an agent_call rather than a plain tool_call: step_1 always outputs \"success\", so check always routes to call_greeter, spawning a real child run (greeter.json) as the result of a branching decision. Confirms canEnqueueStep's generic dependency check and spawnChildRun work together correctly when the step becoming enqueable is an agent_call, not just when it is enqueued unconditionally from the top of a run.").
		WithOutputStep(callGreeter)

	data, err := agent.JSON()
	if err != nil {
		fail(err)
	}
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	if err := os.WriteFile("agents/conditional_into_agent_call.json", withSchema, 0644); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/conditional_into_agent_call.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
