package tools

import (
	"context"
	"strings"
	"testing"
)

// newTestPandocToMarkdown skips the test if pandoc isn't installed,
// rather than mocking the subprocess boundary - the whole point of
// this tool is a real external binary, so testing against a fake one
// would prove nothing about the actual mechanism being exercised.
func newTestPandocToMarkdown(t *testing.T) *PandocToMarkdown {
	t.Helper()
	tool, err := NewPandocToMarkdown()
	if err != nil {
		t.Skipf("pandoc not installed, skipping: %v", err)
	}
	return tool
}

func TestPandocToMarkdown_Success(t *testing.T) {
	tool := newTestPandocToMarkdown(t)

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

func TestPandocToMarkdown_EmptyInput(t *testing.T) {
	tool := newTestPandocToMarkdown(t)

	output, err := tool.Run(context.Background(), "")
	if err != nil {
		t.Fatalf("unexpected error for empty input: %v", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Errorf("expected empty output for empty input, got: %q", output)
	}
}

func TestPandocToMarkdown_ContextCancellation(t *testing.T) {
	newTestPandocToMarkdown(t) // only to trigger the skip if pandoc isn't installed

	tool, err := NewPandocToMarkdown()
	if err != nil {
		t.Skipf("pandoc not installed, skipping: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already canceled before Run even starts

	_, err = tool.Run(ctx, "<p>test</p>")
	if err == nil {
		t.Fatal("expected an error when context is already canceled, got none")
	}
}

func TestNewPandocToMarkdown_NotFound(t *testing.T) {
	_, err := newPandocToMarkdown("definitely-not-a-real-binary-xyz123")
	if err == nil {
		t.Fatal("expected an error for a binary that doesn't exist on PATH, got none")
	}
}
