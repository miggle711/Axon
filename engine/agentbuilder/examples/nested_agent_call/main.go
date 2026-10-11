// Command nested_agent_call tests agent_call nesting two levels deep:
// a grandparent calls a parent via agent_call, and that parent itself
// calls greeter via agent_call. greeter_caller.json already proves
// depth 1 works (a top-level run calling greeter), but nothing calls
// greeter_caller itself as a sub-agent, so depth 2 is untested.
//
// What this actually exercises: spawnChildRun spawning a child run
// whose own steps include another agent_call, meaning a grandchild
// run gets spawned from inside a child run, not just from a top-level
// run. Whether output_step propagation correctly chains through two
// levels (grandparent reads parent's output_step, which itself is the
// result of parent's own child call) is the real, previously untested
// question.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/nested_agent_call
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	if err := writeParentAgent(); err != nil {
		fail(err)
	}
	if err := writeGrandparentAgent(); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/nested_agent_call_parent.json and engine/agents/nested_agent_call_grandparent.json")
}

// writeParentAgent mirrors greeter_caller.json's shape exactly, but
// with output_step set so it can itself be called as a sub-agent,
// which greeter_caller.json never needed.
func writeParentAgent() error {
	call := ab.AgentCall("call_greeter", "greeter", "{{user_input}}")
	after := ab.Tool("after_greeting", "echo", "greeter said: "+ab.Output(call), call)

	agent := ab.New("nested_agent_call_parent", call, after).
		WithDescription("Middle layer of the two-level agent_call nesting test. Same shape as greeter_caller.json (calls greeter, then echoes its result), but with output_step set so a grandparent can call this as a sub-agent in turn.").
		WithOutputStep(after)

	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/nested_agent_call_parent.json", data)
}

// writeGrandparentAgent calls the parent above via agent_call, one
// level up from greeter_caller.json's own single level of nesting.
func writeGrandparentAgent() error {
	call := ab.AgentCall("call_parent", "nested_agent_call_parent", "{{user_input}}")
	after := ab.Tool("after_call", "echo", "parent returned: "+ab.Output(call), call)

	agent := ab.New("nested_agent_call_grandparent", call, after).
		WithDescription("Top layer of the two-level agent_call nesting test: calls nested_agent_call_parent.json, which itself calls greeter.json. Confirms output_step propagation correctly chains through two levels of agent_call nesting, not just one.").
		WithOutputStep(after)

	data, err := agent.JSON()
	if err != nil {
		return err
	}
	return writeWithSchema("agents/nested_agent_call_grandparent.json", data)
}

func writeWithSchema(path string, data []byte) error {
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	return os.WriteFile(path, withSchema, 0644)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
