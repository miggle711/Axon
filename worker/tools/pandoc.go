package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
)

// PandocToMarkdown converts input (HTML) to Markdown by shelling out to
// a real pandoc binary. First concrete case for #42's subprocess
// extensibility direction - a tool whose actual behavior lives in
// external, non-Go software the worker doesn't link against, run as a
// child process rather than called over HTTP like tavily_search.
//
// Deliberately hard-coded (command, format flags) rather than
// config-driven: proving the subprocess execution mechanism itself
// (spawn, pipe stdin/stdout, surface stderr on failure, respect ctx)
// is correct comes before designing a generic manifest format for
// declaring arbitrary subprocess tools - the same order #36's
// tavily_search was built in before #42 ever asked whether tools
// should be more than compiled-in Go.
type PandocToMarkdown struct {
	// binaryPath is the resolved path to pandoc, found once at startup
	// (see NewPandocToMarkdown) rather than re-resolved via PATH on
	// every call.
	binaryPath string
}

// NewPandocToMarkdown resolves pandoc on PATH and returns a ready tool,
// or an error if it's not installed - checked once at worker startup
// (mirroring tavily_search's API-key check) so a missing dependency is
// a clear startup warning, not a confusing per-call failure later.
func NewPandocToMarkdown() (*PandocToMarkdown, error) {
	return newPandocToMarkdown("pandoc")
}

// newPandocToMarkdown takes the binary name as a parameter so the
// not-found path is actually testable (pointed at a name that can't
// exist) without depending on pandoc being absent from the real PATH.
func newPandocToMarkdown(binaryName string) (*PandocToMarkdown, error) {
	path, err := exec.LookPath(binaryName)
	if err != nil {
		return nil, fmt.Errorf("%s not found on PATH: %w", binaryName, err)
	}
	return &PandocToMarkdown{binaryPath: path}, nil
}

func (p *PandocToMarkdown) Run(ctx context.Context, input string) (string, error) {
	cmd := exec.CommandContext(ctx, p.binaryPath, "-f", "html", "-t", "markdown")
	cmd.Stdin = bytes.NewReader([]byte(input))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("pandoc_to_markdown: %w: %s", err, stderr.String())
	}

	return stdout.String(), nil
}
