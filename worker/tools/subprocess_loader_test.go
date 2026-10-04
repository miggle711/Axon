package tools

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
)

var discardLogger = slog.New(slog.DiscardHandler)

func writeToolConfig(t *testing.T, dir, filename, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, filename), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", filename, err)
	}
}

func TestLoadSubprocessToolsFromDir(t *testing.T) {
	dir := t.TempDir()

	// "cat" exists on any platform this test suite runs on, unlike
	// pandoc, so this doesn't need a pandoc-specific skip to prove the
	// loader itself works.
	writeToolConfig(t, dir, "cat_tool.json", `{"name": "cat_tool", "command": "cat", "args": []}`)
	writeToolConfig(t, dir, "missing_binary.json", `{"name": "missing_binary", "command": "definitely-not-a-real-binary-xyz123", "args": []}`)
	// Non-JSON and dotfiles should be ignored, not error.
	if err := os.WriteFile(filepath.Join(dir, "README.md"), []byte("not a tool"), 0644); err != nil {
		t.Fatalf("failed to write README.md: %v", err)
	}
	writeToolConfig(t, dir, ".hidden.json", `{"name": "hidden", "command": "cat"}`)

	registry, err := LoadSubprocessToolsFromDir(dir, discardLogger)
	if err != nil {
		t.Fatalf("LoadSubprocessToolsFromDir failed: %v", err)
	}

	if len(registry) != 1 {
		t.Fatalf("expected only cat_tool to load (missing_binary's command doesn't exist, hidden/README should be skipped), got %d: %v", len(registry), registry)
	}
	if _, ok := registry["cat_tool"]; !ok {
		t.Error("expected cat_tool to be registered")
	}
	if _, ok := registry["missing_binary"]; ok {
		t.Error("expected missing_binary to be skipped, not registered, since its command doesn't exist")
	}
	if _, ok := registry["hidden"]; ok {
		t.Error("expected .hidden.json to be skipped")
	}
}

func TestLoadSubprocessToolsFromDir_MalformedFile(t *testing.T) {
	dir := t.TempDir()
	writeToolConfig(t, dir, "broken.json", `not json`)

	if _, err := LoadSubprocessToolsFromDir(dir, discardLogger); err == nil {
		t.Fatal("expected an error for malformed JSON, got none")
	}
}

func TestLoadSubprocessToolsFromDir_MissingRequiredFields(t *testing.T) {
	t.Run("missing name", func(t *testing.T) {
		dir := t.TempDir()
		writeToolConfig(t, dir, "no_name.json", `{"command": "cat"}`)
		if _, err := LoadSubprocessToolsFromDir(dir, discardLogger); err == nil {
			t.Fatal("expected an error for a config missing name, got none")
		}
	})

	t.Run("missing command", func(t *testing.T) {
		dir := t.TempDir()
		writeToolConfig(t, dir, "no_command.json", `{"name": "no_command"}`)
		if _, err := LoadSubprocessToolsFromDir(dir, discardLogger); err == nil {
			t.Fatal("expected an error for a config missing command, got none")
		}
	})
}

func TestLoadSubprocessToolsFromDir_MissingDir(t *testing.T) {
	if _, err := LoadSubprocessToolsFromDir("/does/not/exist", discardLogger); err == nil {
		t.Fatal("expected an error for a nonexistent directory, got none")
	}
}

// TestLoadSubprocessToolsFromDir_DuplicateName covers a real review
// finding: two config files both naming the same tool used to
// silently let the second one clobber the first with no warning at
// all. Now a hard error, same as LoadAgentsFromDir already treats a
// duplicate agent ID.
func TestLoadSubprocessToolsFromDir_DuplicateName(t *testing.T) {
	dir := t.TempDir()
	writeToolConfig(t, dir, "a.json", `{"name": "dup_tool", "command": "cat", "args": []}`)
	writeToolConfig(t, dir, "b.json", `{"name": "dup_tool", "command": "cat", "args": ["-n"]}`)

	if _, err := LoadSubprocessToolsFromDir(dir, discardLogger); err == nil {
		t.Fatal("expected an error for two config files claiming the same tool name, got none")
	}
}

// TestLoadSubprocessToolsFromDir_RealPandocConfig covers the actual
// committed worker/subprocess_tools/pandoc_to_markdown.json, proving
// the real config file this repo ships (not just a synthetic test
// fixture) loads and produces a working tool.
func TestLoadSubprocessToolsFromDir_RealPandocConfig(t *testing.T) {
	registry, err := LoadSubprocessToolsFromDir("../subprocess_tools", discardLogger)
	if err != nil {
		t.Fatalf("LoadSubprocessToolsFromDir failed: %v", err)
	}

	tool, ok := registry["pandoc_to_markdown"]
	if !ok {
		t.Skip("pandoc not installed, pandoc_to_markdown was skipped rather than registered")
	}

	output, err := tool.Run(context.Background(), "<p>hi</p>")
	if err != nil {
		t.Fatalf("Run failed: %v", err)
	}
	if output == "" {
		t.Error("expected non-empty output")
	}
}
