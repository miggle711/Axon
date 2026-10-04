package cli

import (
	ab "axon-engine/agentbuilder"
	"fmt"
)

// ScaffoldTypes lists the scaffold shapes axon init supports, in the
// order they're listed in --help output.
var ScaffoldTypes = []string{"tool_call", "conditional", "supervisor", "agent_call", "fan_in"}

// Scaffold builds a starter agent for one of ScaffoldTypes, named
// name, using engine/agentbuilder rather than hand-maintaining
// equivalent JSON strings here. That's the same package a real agent
// author would reach for next, so the scaffold this prints is built
// the exact way #54 wants someone to go on building it themselves,
// not a separate, parallel representation that could drift from what
// the builder actually produces.
//
// Each shape mirrors a real pattern already proven elsewhere in this
// repo (see the comment on each case below) rather than an invented
// one, so what axon init hands back is a shape known to actually work,
// not just something that happens to validate.
func Scaffold(scaffoldType, name string) ([]byte, error) {
	switch scaffoldType {
	case "tool_call":
		return scaffoldToolCall(name)
	case "conditional":
		return scaffoldConditional(name)
	case "supervisor":
		return scaffoldSupervisor(name)
	case "agent_call":
		return scaffoldAgentCall(name)
	case "fan_in":
		return scaffoldFanIn(name)
	default:
		return nil, fmt.Errorf("unknown scaffold type %q (must be one of %v)", scaffoldType, ScaffoldTypes)
	}
}

// scaffoldToolCall mirrors engine/agents/greeter.json's shape: the
// simplest possible agent, one tool_call step.
func scaffoldToolCall(name string) ([]byte, error) {
	step := ab.Tool("step_1", "echo", "{{user_input}}")
	agent := ab.New(name, step).
		WithDescription("Starter scaffold (axon init --type tool_call): a single tool_call step. Replace \"echo\" with a real tool and edit input_template, or add more steps and depends_on to build a real chain.").
		WithOutputStep(step)
	return agent.JSON()
}

// scaffoldConditional mirrors engine's own
// conditionalTestAgentSteps shape (engine/orchestrator_test.go): a
// step, a conditional branching on its output, two targets.
func scaffoldConditional(name string) ([]byte, error) {
	step1 := ab.Tool("step_1", "echo", "success")
	onTrue := ab.Tool("on_true_step", "echo", "took the true branch")
	onFalse := ab.Tool("on_false_step", "echo", "took the false branch")
	check := ab.Conditional("check", ab.Output(step1)+" == success", onTrue, onFalse, step1)

	// No WithOutputStep here on purpose. Only one of on_true_step and
	// on_false_step actually runs per run, and agent_call's
	// output_step can only ever name one fixed step (this is the
	// fourth constraint tracked in #56). Picking either branch here
	// would misrepresent which one produced the real result whenever
	// the other branch is the one that actually ran.
	agent := ab.New(name, step1, check, onTrue, onFalse).
		WithDescription("Starter scaffold (axon init --type conditional): step_1 runs, check compares its output with == (also supports != and contains), routing to on_true_step or on_false_step. Replace the tools, the condition, and the branch steps with real ones. No output_step is set here since only one branch actually runs per run; set it yourself once you know which branch matters, or leave it unset if this agent is never called as a sub-agent.")
	return agent.JSON()
}

// scaffoldSupervisor mirrors research_agent.json's shape (minus the
// real Tavily call): a supervisor whose first move is forced
// structurally via FirstIteration (#56) rather than relying on prompt
// text, looping on one option until the model says "done".
func scaffoldSupervisor(name string) ([]byte, error) {
	option := ab.Tool("search", "echo", "{{user_input}}")
	supervisor := ab.Supervisor("judge", "You are deciding whether the results so far are sufficient to answer the question.\n\nQuestion: {{user_input}}\n\nMost recent result:\n"+ab.Output(option)+"\n\nIf sufficient, respond with exactly: done\nOtherwise, respond with exactly: search\n\nRespond with only one of those two words, nothing else.",
		[]*ab.Step{option}).FirstIteration(option)

	agent := ab.New(name, supervisor, option).
		WithDescription("Starter scaffold (axon init --type supervisor): judge forces its first move to search structurally (FirstIteration/first_iteration_step, see #56), then makes a real LLM decision on later iterations until it answers done. Replace \"echo\" with a real tool, and judge's prompt_template with real decision logic. Requires GROQ_API_KEY on the worker once judge actually loops.").
		WithOutputStep(supervisor)
	return agent.JSON()
}

// scaffoldAgentCall mirrors greeter_caller.json's shape: one agent_call
// step spawning a sub-agent, then a step using its result.
//
// The referenced sub-agent name is a placeholder, same spirit as
// tool_call's scaffold naming "echo" as a placeholder tool: a brand
// new agent_call scaffold has no other agent to actually point at yet,
// so this names one clearly and says so in the description rather than
// guessing at a real agent that doesn't exist.
func scaffoldAgentCall(name string) ([]byte, error) {
	call := ab.AgentCall("call_sub_agent", "replace_with_real_agent_name", "{{user_input}}")
	afterCall := ab.Tool("after_call", "echo", "sub-agent said: "+ab.Output(call), call)

	agent := ab.New(name, call, afterCall).
		WithDescription("Starter scaffold (axon init --type agent_call): calls a sub-agent, then uses its result. \"replace_with_real_agent_name\" is a placeholder, not a real agent; this won't actually run until it's set to an agent name that's registered in engine/agents/ (and that agent needs an output_step set, required for anything called this way).").
		WithOutputStep(afterCall)
	return agent.JSON()
}

// scaffoldFanIn mirrors multi_source_research_agent.json's shape: two
// independent steps (no depends_on between them, so the engine runs
// them in parallel, not sequentially) feeding a third.
func scaffoldFanIn(name string) ([]byte, error) {
	branchA := ab.Tool("branch_a", "echo", "{{user_input}} (branch a)")
	branchB := ab.Tool("branch_b", "echo", "{{user_input}} (branch b)")
	combine := ab.LLM("combine",
		"Combine these two results.\n\nBranch A:\n"+ab.Output(branchA)+"\n\nBranch B:\n"+ab.Output(branchB),
		branchA, branchB,
	)

	agent := ab.New(name, branchA, branchB, combine).
		WithDescription("Starter scaffold (axon init --type fan_in): branch_a and branch_b have no depends_on between them, so the engine runs them in parallel; combine waits on both. Replace the tools and the combining prompt with real ones. Requires GROQ_API_KEY on the worker since combine is an llm_call.").
		WithOutputStep(combine)
	return agent.JSON()
}
