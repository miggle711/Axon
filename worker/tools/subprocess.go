package tools

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"time"
)

// maxSubprocessOutputBytes caps how much stdout/stderr a single call
// captures. Without a limit, a misbehaving (or, per #42's documented
// trust boundary, intentionally hostile) command could write arbitrary
// amounts of data and exhaust worker memory - the timeout alone
// doesn't protect against this, since a command can write quickly and
// still finish well within it.
const maxSubprocessOutputBytes = 10 * 1024 * 1024 // 10 MiB

// limitedBuffer stops accepting writes once more than limit bytes have
// been written to it, instead of growing unbounded, and calls
// onExceeded the first time that happens.
//
// Deliberately holds its buffer as an unexported field rather than
// embedding bytes.Buffer: os/exec special-cases an exec.Cmd.Stdout/
// Stderr that is (or embeds) a *bytes.Buffer, writing into its
// internal slice directly via an internal fast path that completely
// bypasses any overridden Write method - confirmed live, embedding let
// os/exec write past the limit entirely uncapped, Write was never even
// called. Composition instead of embedding closes that bypass, since
// the concrete type os/exec sees is no longer a *bytes.Buffer at all.
//
// Returning an error from Write alone also isn't enough to actually
// stop a runaway command: os/exec copies a pipe into Stdout/Stderr on
// a background goroutine, and a Write error just makes that goroutine
// stop reading - it doesn't touch the process itself. A command that
// keeps writing to a pipe nobody's draining anymore just blocks on its
// own write() once the OS pipe buffer fills, so the call would hang
// until the overall timeout anyway rather than actually failing fast
// on the output limit. onExceeded is used to kill the process
// directly instead (see Run), reusing the same WaitDelay-protected
// shutdown path the timeout already relies on.
type limitedBuffer struct {
	buf        bytes.Buffer
	limit      int
	onExceeded func()
	exceeded   bool
	totalSeen  int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	b.totalSeen += len(p)
	if b.totalSeen > b.limit {
		if !b.exceeded && b.onExceeded != nil {
			b.onExceeded()
		}
		b.exceeded = true
		return 0, fmt.Errorf("output exceeded %d byte limit", b.limit)
	}
	return b.buf.Write(p)
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}

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
	// Without WaitDelay, Wait (called inside Run) can block past the
	// context's cancellation waiting for stdout/stderr pipes to close -
	// a command that forks its own children (unlike pandoc, but a
	// future config's command might) can keep those pipes open well
	// after the parent itself was killed, silently defeating the
	// timeout above. WaitDelay forces the pipes closed shortly after
	// cancellation regardless.
	cmd.WaitDelay = 5 * time.Second

	stdout := &limitedBuffer{limit: maxSubprocessOutputBytes, onExceeded: cancel}
	stderr := &limitedBuffer{limit: maxSubprocessOutputBytes, onExceeded: cancel}
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	if err := cmd.Run(); err != nil {
		if stdout.exceeded || stderr.exceeded {
			return "", fmt.Errorf("%s: output exceeded %d byte limit", t.Name, maxSubprocessOutputBytes)
		}
		return "", fmt.Errorf("%s: %w: %s", t.Name, err, stderr.String())
	}

	return stdout.String(), nil
}
