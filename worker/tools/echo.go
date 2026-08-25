package tools

import "context"

// Echo returns its input unchanged. Used to prove the orchestration
// mechanism works independent of any real tool's behavior (see #2),
// and kept as a real registered tool so agents built against it
// continue to work now that tool_call dispatches by name.
type Echo struct{}

func (Echo) Run(ctx context.Context, input string) (string, error) {
	return input, nil
}
