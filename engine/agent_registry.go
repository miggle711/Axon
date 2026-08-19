package engine

// AgentRegistry resolves an agent name (StepDefinition.Agent) to its
// AgentDefinition, so an agent_call step can spawn the right child run.
type AgentRegistry interface {
	Get(name string) (AgentDefinition, bool)
}

// MapAgentRegistry is an AgentRegistry backed by a fixed, in-memory map.
type MapAgentRegistry map[string]AgentDefinition

func (r MapAgentRegistry) Get(name string) (AgentDefinition, bool) {
	def, ok := r[name]
	return def, ok
}
