// Package agentbuilder is a typed Go API for constructing
// engine.AgentDefinition values, meant to be run once (go run ./...)
// to generate the plain JSON an agent is actually authored as - the
// engine only ever loads JSON (engine.LoadAgentsFromDir), that stays
// unchanged, so the zero-rebuild property #7 chose JSON for in the
// first place is untouched. This package exists only to make writing
// that JSON correctly less error-prone.
//
// The concrete gap this closes: hand-written agent JSON refers to
// other steps by bare string (depends_on, on_true, on_false, options,
// first_iteration_step, and {{step_id.output}}/{{step_id.iteration}}
// template placeholders), and a typo in any of those only ever
// surfaces once validateAgentDefinition runs at engine startup - not
// while actually writing the file. Here, a step is a *Step value
// created once and referenced by variable from then on, so a typo'd
// reference is an undefined-variable compile error instead of a
// silently-wrong string.
//
// It also closes a second, real, previously undocumented footgun: a
// conditional's OnTrue/OnFalse and a supervisor's Options name target
// steps, but each target step separately needs the reverse edge in its
// own DependsOn too (see engine/orchestrator_test.go's
// conditionalTestAgentSteps/supervisorTestAgentSteps for the exact
// hand-written shape this mirrors) - miss the second half by hand and
// the step either never becomes enqueable or enqueues too early. Build
// wires both directions automatically from one declaration.
//
// Doesn't attempt compile-time checking of template text itself
// (prompt_template/input_template/condition are still plain strings) -
// only Output/Iteration generate the {{...}} placeholders themselves
// correctly from a *Step reference; the surrounding prose is still
// free text, same as hand-written JSON.
package agentbuilder

import (
	engine "axon-engine"
	"fmt"
)

// Step is one node under construction. Obtained from one of Tool, LLM,
// Conditional, AgentCall, or Supervisor, and referenced by variable
// from then on - never by its string ID - everywhere another step
// needs to point at it.
type Step struct {
	def engine.StepDefinition
}

// ID returns the step's own ID, for the rare case code needs it as a
// plain string (e.g. building a template fragment Output/Iteration
// don't cover). Most agents never need this directly.
func (s *Step) ID() string {
	return s.def.ID
}

// dependsOn records that s must wait on each of deps, used both by the
// direct constructors (Tool, LLM, ...) for the dependencies an author
// passes explicitly, and internally by Conditional/Supervisor to wire
// the reverse edge into each of their target steps automatically.
func (s *Step) dependsOn(deps ...*Step) {
	for _, dep := range deps {
		s.def.DependsOn = append(s.def.DependsOn, dep.def.ID)
	}
}

func ids(steps []*Step) []string {
	out := make([]string, len(steps))
	for i, s := range steps {
		out[i] = s.def.ID
	}
	return out
}

// Output returns the {{step.output}} template placeholder referencing
// step's resolved output, for use inside a prompt_template,
// input_template, or condition string. Generated from step's actual
// ID, so the only way to get this wrong is to reference the wrong
// *Step variable - which the Go compiler already catches as an
// undefined-variable error, unlike a hand-typed {{...}} string.
func Output(step *Step) string {
	return fmt.Sprintf("{{%s.output}}", step.def.ID)
}

// Iteration returns the {{step.iteration}} template placeholder for a
// supervisor step's own current loop count (see #56) - 0 before its
// first decision. Only meaningful on a step built with Supervisor, but
// (matching validateAgentDefinition's existing leniency) this isn't
// enforced here.
func Iteration(step *Step) string {
	return fmt.Sprintf("{{%s.iteration}}", step.def.ID)
}

// Tool builds a tool_call step: dispatches to the registered tool
// named tool, with inputTemplate as its (resolved) input. deps are
// steps that must complete before this one becomes runnable.
func Tool(id, tool, inputTemplate string, deps ...*Step) *Step {
	s := &Step{def: engine.StepDefinition{
		ID:            id,
		Type:          engine.StepTypeToolCall,
		Tool:          tool,
		InputTemplate: inputTemplate,
		DependsOn:     []string{},
	}}
	s.dependsOn(deps...)
	return s
}

// LLM builds an llm_call step: sends promptTemplate (resolved) to
// Groq, records the completion as output.
func LLM(id, promptTemplate string, deps ...*Step) *Step {
	s := &Step{def: engine.StepDefinition{
		ID:             id,
		Type:           engine.StepTypeLLMCall,
		PromptTemplate: promptTemplate,
		DependsOn:      []string{},
	}}
	s.dependsOn(deps...)
	return s
}

// AgentCall builds an agent_call step: spawns agentName as a child
// run, resolving inputTemplate as that child run's input.
func AgentCall(id, agentName, inputTemplate string, deps ...*Step) *Step {
	s := &Step{def: engine.StepDefinition{
		ID:            id,
		Type:          engine.StepTypeAgentCall,
		Agent:         agentName,
		InputTemplate: inputTemplate,
		DependsOn:     []string{},
	}}
	s.dependsOn(deps...)
	return s
}

// Conditional builds a conditional step: evaluates condition (an ==,
// !=, or contains comparison, e.g. Output(step)+" == success"),
// routing to onTrue or onFalse. Either may be nil for a branch that
// intentionally does nothing (engine/validate.go treats an empty
// on_true/on_false as valid for exactly this reason).
//
// Automatically adds this conditional as a dependency of onTrue and
// onFalse, in addition to recording them on the conditional itself -
// the reverse edge engine/orchestrator_test.go's
// conditionalTestAgentSteps shows is needed on the target step's own
// DependsOn, which hand-written JSON has to remember to add
// separately and this builder no longer requires remembering.
func Conditional(id, condition string, onTrue, onFalse *Step, deps ...*Step) *Step {
	s := &Step{def: engine.StepDefinition{
		ID:        id,
		Type:      engine.StepTypeConditional,
		Condition: condition,
		DependsOn: []string{},
	}}
	if onTrue != nil {
		s.def.OnTrue = onTrue.def.ID
		onTrue.dependsOn(s)
	}
	if onFalse != nil {
		s.def.OnFalse = onFalse.def.ID
		onFalse.dependsOn(s)
	}
	s.dependsOn(deps...)
	return s
}

// Supervisor builds a supervisor step: sends promptTemplate to Groq,
// which picks one of options to run next, looping until it answers
// "done" or the iteration cap is hit (engine.MaxSupervisorIterations).
//
// Automatically adds this supervisor as a dependency of every step in
// options, the same reverse-edge wiring Conditional does - mirroring
// engine/orchestrator_test.go's supervisorTestAgentSteps, where
// option_a/option_b each separately need the supervisor in their own
// DependsOn.
func Supervisor(id, promptTemplate string, options []*Step, deps ...*Step) *Step {
	s := &Step{def: engine.StepDefinition{
		ID:             id,
		Type:           engine.StepTypeSupervisor,
		PromptTemplate: promptTemplate,
		Options:        ids(options),
		DependsOn:      []string{},
	}}
	for _, opt := range options {
		opt.dependsOn(s)
	}
	s.dependsOn(deps...)
	return s
}

// FirstIteration sets step's first_iteration_step (see #56): on this
// supervisor's first iteration, option runs automatically, skipping
// the LLM decision call for that one iteration. option must already be
// one of the Options passed to Supervisor - returns step unchanged
// (does not validate this here) so it chains naturally:
//
//	judge := Supervisor("judge", prompt, []*Step{search}).FirstIteration(search)
//
// validateAgentDefinition still enforces option is actually one of
// step's own Options when the generated JSON is loaded - this is a
// convenience for chaining, not a second source of truth for that
// rule.
func (s *Step) FirstIteration(option *Step) *Step {
	s.def.FirstIterationStep = option.def.ID
	return s
}
