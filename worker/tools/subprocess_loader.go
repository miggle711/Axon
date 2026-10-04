package tools

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// LoadSubprocessToolsFromDir reads every *.json file directly under
// dir, except dotfiles, unmarshals each into a SubprocessTool, resolves
// its Command on PATH, and returns the ones that resolved successfully
// as a map keyed by Name - mirroring engine.LoadAgentsFromDir's
// directory-of-JSON-files convention, for the same reason: dropping a
// new tool config file into dir should be enough to register it, no
// Go code change or rebuild required (#42).
//
// A tool config whose binary isn't installed is logged as a warning
// and skipped, exactly like pandoc_to_markdown's own startup check and
// tavily_search's API-key check - a missing dependency shouldn't stop
// the whole worker from starting, only the one tool that needs it.
// A malformed config file (bad JSON, missing name/command) is a hard
// error, same as LoadAgentsFromDir treats a malformed agent file -
// that's an authoring mistake worth failing loudly on, not silently
// skipping.
func LoadSubprocessToolsFromDir(dir string, logger *slog.Logger) (map[string]Tool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("failed to read tool config directory %s: %w", dir, err)
	}

	registry := make(map[string]Tool)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".json") || strings.HasPrefix(name, ".") {
			continue
		}

		path := filepath.Join(dir, name)
		data, err := os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("failed to read tool config file %s: %w", path, err)
		}

		var tool SubprocessTool
		if err := json.Unmarshal(data, &tool); err != nil {
			return nil, fmt.Errorf("failed to parse tool config file %s: %w", path, err)
		}
		if tool.Name == "" {
			return nil, fmt.Errorf("tool config file %s: missing required field \"name\"", path)
		}
		if tool.Command == "" {
			return nil, fmt.Errorf("tool config file %s: missing required field \"command\"", path)
		}

		if err := tool.resolve(); err != nil {
			logger.Warn("subprocess tool not available, skipping", "name", tool.Name, "config_file", path, "error", err)
			continue
		}

		registry[tool.Name] = &tool
	}

	return registry, nil
}
