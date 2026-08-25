// Package tools provides a registry mapping a StepDefinition.Tool name
// to an executable implementation, for tool_call steps.
package tools

import "context"

// Tool executes a single tool call with the given input and returns
// its output text.
type Tool interface {
	Run(ctx context.Context, input string) (string, error)
}
