// Package llm provides a provider-agnostic interface for completing
// prompts, so worker code that needs an LLM doesn't depend on any one
// provider's SDK or wire format.
package llm

import "context"

// Client completes a single prompt and returns the model's response text.
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)
}
