package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentsFromDir reads every *.json file directly under dir, except
// dotfiles and *.schema.json (meta files describing the agent format
// itself, like agent.schema.json, not an agent to register), unmarshals
// each into an AgentDefinition, and returns them as a MapAgentRegistry
// keyed by filename (without the .json extension) — e.g.
// agents/research_agent.json is registered as "research_agent". Used to
// populate agent_call's AgentRegistry once at startup.
func LoadAgentsFromDir(dir string) (MapAgentRegistry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory %s: %w", dir, err)
	}

	registry := MapAgentRegistry{}
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".schema.json") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read agent file %s: %w", path, err)
		}

		var def AgentDefinition
		if err := json.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("failed to parse agent file %s: %w", path, err)
		}

		registry[strings.TrimSuffix(name, ".json")] = def
	}

	return registry, nil
}
