package engine

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// LoadAgentsFromDir reads every *.json file directly under dir,
// unmarshals each into an AgentDefinition, and returns them as a
// MapAgentRegistry keyed by filename (without the .json extension) —
// e.g. agents/research_agent.json is registered as "research_agent".
// Used to populate agent_call's AgentRegistry once at startup.
func LoadAgentsFromDir(dir string) (MapAgentRegistry, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read agents directory %s: %w", dir, err)
	}

	registry := MapAgentRegistry{}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}

		path := filepath.Join(dir, entry.Name())
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read agent file %s: %w", path, err)
		}

		var def AgentDefinition
		if err := json.Unmarshal(data, &def); err != nil {
			return nil, fmt.Errorf("failed to parse agent file %s: %w", path, err)
		}

		name := strings.TrimSuffix(entry.Name(), ".json")
		registry[name] = def
	}

	return registry, nil
}
