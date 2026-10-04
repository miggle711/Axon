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

// TestLimitedBuffer covers the capping logic in isolation, no
// subprocess involved - the actual review finding was that stdout and
// stderr grew unbounded, so this confirms a write past the limit
// errors instead of silently succeeding.
func TestLimitedBuffer(t *testing.T) {
	t.Run("writes within the limit succeed", func(t *testing.T) {
		b := &limitedBuffer{limit: 10}
		if _, err := b.Write([]byte("hello")); err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if b.String() != "hello" {
			t.Errorf("got %q, want %q", b.String(), "hello")
		}
	})

	t.Run("a write pushing past the limit errors", func(t *testing.T) {
		b := &limitedBuffer{limit: 5}
		if _, err := b.Write([]byte("way too long")); err == nil {
			t.Fatal("expected an error for a write exceeding the limit, got none")
		}
		if !b.exceeded {
			t.Error("expected exceeded to be set")
		}
	})
}

// TestSubprocessTool_OutputLimit proves the limit is actually wired
// into Run, not just correct in isolation: a real command writing more
// than the (test-sized) limit should fail the call fast, not just
// eventually. The timing assertion matters here, not just the error:
// an earlier version of this fix embedded bytes.Buffer directly in
// limitedBuffer, which let os/exec write straight into its internal
// slice via an internal fast path that bypasses Write entirely - the
// limit was never actually enforced, the call only failed once (and
// because) it hit the unrelated overall timeout. A test asserting only
// "an error happened" passed against that broken version just as
// happily as against the real fix, since both eventually error - only
// checking that it happens quickly (well under the deliberately
// generous timeout) actually distinguishes "the cap worked" from "the
// cap did nothing and the timeout saved us instead".
func TestSubprocessTool_OutputLimit(t *testing.T) {
	tool := &SubprocessTool{Name: "noisy_tool", Command: "yes", Args: []string{}, TimeoutSeconds: 10}
	if err := tool.resolve(); err != nil {
		t.Skipf("yes not found on PATH, skipping: %v", err)
	}

	start := time.Now()
	_, err := tool.Run(context.Background(), "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from a command that never stops producing output, got none")
	}
	if elapsed > 3*time.Second {
		t.Errorf("expected the output limit to fail the call well before the 10s timeout, took %v - if this is close to 10s, the limit probably isn't actually being enforced and the timeout is doing the real work instead", elapsed)
	}
}

func TestSubprocessTool_ResolveNotFound(t *testing.T) {
	tool := &SubprocessTool{Name: "nope", Command: "definitely-not-a-real-binary-xyz123"}
	if err := tool.resolve(); err == nil {
		t.Fatal("expected an error for a command that doesn't exist on PATH, got none")
	}
}
