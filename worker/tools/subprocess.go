package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// SubprocessTool runs a fixed external command, feeding Run's input to
// its stdin and returning its stdout as output - the generic version
// of #74's pandoc_to_markdown, config-driven instead of hard-coded in
// Go (#42). Covers a tool's static-command-plus-flags shape; a tool
// needing input interpolated into its arguments, multiple input files,
// or a long-lived process reused across calls is out of scope here and
// still needs real Go (or further design, once a real case for it
// exists - the same discipline this project uses everywhere else).
type SubprocessTool struct {
	// Name is this tool's registry key, e.g. "pandoc_to_markdown".
	Name string `json:"name"`
	// Command is the binary to run, resolved via PATH at load time
	// (mirroring tavily_search's API-key check and pandoc_to_markdown's
	// own PATH lookup) so a missing binary is a clear startup warning,
	// not a confusing per-call failure.
	Command string `json:"command"`
	// Args are passed to Command unchanged on every call. Input is
	// always delivered via stdin, never substituted into Args - a tool
	// needing that is a real Go tool, not a SubprocessTool (see above).
	Args []string `json:"args"`
	// TimeoutSeconds bounds how long a single call may run before
	// being killed. Defaults to defaultSubprocessTimeout if zero or
	// unset, so a hung external process can't block a worker slot
	// forever.
	TimeoutSeconds int `json:"timeout_seconds,omitempty"`

	// binaryPath is Command resolved to an absolute path once at load
	// time, same as pandoc_to_markdown's binaryPath.
	binaryPath string
}

const defaultSubprocessTimeout = 30 * time.Second

// resolve looks up t.Command on PATH, returning an error if it's not
// installed. Called once per tool at load time (see
// LoadSubprocessToolsFromDir), not on every Run call.
func (t *SubprocessTool) resolve() error {
	path, err := exec.LookPath(t.Command)
	if err != nil {
		return fmt.Errorf("%s not found on PATH: %w", t.Command, err)
	}
	t.binaryPath = path
	return nil
}

func (t *SubprocessTool) Run(ctx context.Context, input string) (string, error) {
	timeout := defaultSubprocessTimeout
	if t.TimeoutSeconds > 0 {
		timeout = time.Duration(t.TimeoutSeconds) * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, t.binaryPath, t.Args...)
	cmd.Stdin = bytes.NewReader([]byte(input))

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("%s: %w: %s", t.Name, err, stderr.String())
	}

	return stdout.String(), nil
}
