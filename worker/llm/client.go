// Package llm provides a provider-agnostic interface for completing
// prompts, so worker code that needs an LLM doesn't depend on any one
// provider's SDK or wire format.
package llm

import "context"

// Client completes a single prompt and returns the model's response text.
type Client interface {
	Complete(ctx context.Context, prompt string) (string, error)

	// Decide asks the model to choose exactly one of options, using
	// whatever structured-output support the provider offers to
	// constrain the answer at the API level rather than relying on the
	// model to follow a plain-text instruction unaided. The returned
	// string is always one of options; implementations must not return
	// a value outside that set without an error.
	Decide(ctx context.Context, prompt string, options []string) (string, error)
}
