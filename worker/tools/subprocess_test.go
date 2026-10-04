package tools

import (
	"context"
	"strings"
	"testing"
	"time"
)

// newTestPandocSubprocessTool skips the test if pandoc isn't
// installed, rather than mocking the subprocess boundary - the whole
// point of this tool is a real external binary, so a fake one would
// prove nothing about the mechanism being exercised.
func newTestPandocSubprocessTool(t *testing.T) *SubprocessTool {
	t.Helper()
	tool := &SubprocessTool{Name: "pandoc_to_markdown", Command: "pandoc", Args: []string{"-f", "html", "-t", "markdown"}}
	if err := tool.resolve(); err != nil {
		t.Skipf("pandoc not installed, skipping: %v", err)
	}
	return tool
}

func TestSubprocessTool_Success(t *testing.T) {
	tool := newTestPandocSubprocessTool(t)

	output, err := tool.Run(context.Background(), "<h1>Hello</h1><p>This is <strong>bold</strong>.</p>")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(output, "Hello") {
		t.Errorf("expected output to contain the heading text, got: %q", output)
	}
	if !strings.Contains(output, "**bold**") {
		t.Errorf("expected output to render <strong> as markdown bold, got: %q", output)
	}
}

func TestSubprocessTool_EmptyInput(t *testing.T) {
	tool := newTestPandocSubprocessTool(t)

	output, err := tool.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("expected empty output for empty input, got: %q", output)
	}
}

func TestSubprocessTool_ContextCancellation(t *testing.T) {
	tool := newTestPandocSubprocessTool(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before Run even starts

	if _, err := tool.Run(ctx, "<p>test</p>"); err == nil {
		t.Fatal("expected an error when context is already canceled, got none")
	}
}

func TestSubprocessTool_Timeout(t *testing.T) {
	// "sleep" is available in any environment this test suite runs in
	// (unlike pandoc), so this doesn't need the pandoc skip - it proves
	// the timeout mechanism itself, independent of which binary a real
	// tool config happens to name.
	tool := &SubprocessTool{Name: "slow_tool", Command: "sleep", Args: []string{"5"}, TimeoutSeconds: 1}
	if err := tool.resolve(); err != nil {
		t.Skipf("sleep not found on PATH, skipping: %v", err)
	}

	start := time.Now()
	_, err := tool.Run(context.Background(), "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a command exceeding its timeout, got none")
	}
	if elapsed > 3*time.Second {
		t.Errorf("expected the timeout to kill the command well before its natural 5s exit, took %v", elapsed)
	}
}

func TestSubprocessTool_ResolveNotFound(t *testing.T) {
	tool := &SubprocessTool{Name: "nope", Command: "definitely-not-a-real-binary-xyz123"}
	if err := tool.resolve(); err == nil {
		t.Fatal("expected an error for a command that doesn't exist on PATH, got none")
	}
}
