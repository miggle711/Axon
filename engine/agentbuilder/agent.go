package agentbuilder

import (
	engine "axon-engine"
	"encoding/json"
	"fmt"
)

// Agent collects steps into a complete engine.AgentDefinition, in the
// order given - matching the order they'll appear in the generated
// JSON's steps array (purely cosmetic, execution order is driven by
// DependsOn, not array position).
type Agent struct {
	def engine.AgentDefinition
}

// New starts an agent definition named name, with the given steps.
func New(name string, steps ...*Step) *Agent {
	defs := make([]engine.StepDefinition, len(steps))
	for i, s := range steps {
		defs[i] = s.def
	}
	return &Agent{def: engine.AgentDefinition{Name: name, Steps: defs}}
}

// WithDescription sets the agent's description, chaining after New.
func (a *Agent) WithDescription(description string) *Agent {
	a.def.Description = description
	return a
}

// WithOutputStep sets which step's result becomes this agent's output
// when it's spawned as a child run via an agent_call step (see
// engine.AgentDefinition.OutputStep) - only required for agents meant
// to be used as sub-agents.
func (a *Agent) WithOutputStep(step *Step) *Agent {
	a.def.OutputStep = step.def.ID
	return a
}

// Definition returns the built engine.AgentDefinition, e.g. to pass
// directly to engine.NewOrchestrator in a test, or to
// validateAgentDefinition before serializing.
func (a *Agent) Definition() engine.AgentDefinition {
	return a.def
}

// JSON serializes the agent as the same shape
// engine.LoadAgentsFromDir reads from engine/agents/*.json -
// indented, matching this repo's committed agent files, so the output
// of this package is indistinguishable from a hand-written one once
// saved. $schema is not included here since it's specific to where the
// file ends up relative to engine/agents/agent.schema.json - add it
// after writing the file if the output is going into that directory.
func (a *Agent) JSON() ([]byte, error) {
	data, err := json.MarshalIndent(a.def, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("failed to marshal agent %q: %w", a.def.Name, err)
	}
	return append(data, '\n'), nil
}
