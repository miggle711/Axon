// Command html_to_markdown_safe demonstrates the actual intended
// workflow for engine/agentbuilder: write Go using the typed builder
// API, run it once, and commit the JSON it prints - the engine itself
// never imports or runs this package, it only ever loads the plain
// JSON this produces (see agentbuilder's package doc for why).
//
// This agent is new, not a rebuild of an existing one: it converts
// HTML to Markdown via pandoc_to_markdown (#75), then a conditional
// checks whether that came back empty and routes to a fallback message
// instead of silently returning nothing - exercising Conditional's
// automatic reverse-dependency wiring for real, on behavior nothing in
// this repo already had.
//
// Run with:
//
//	go run ./engine/agentbuilder/examples/html_to_markdown_safe > engine/agents/html_to_markdown_safe.json
//
// then add "$schema": "./agent.schema.json" as the file's first key,
// same as every other committed agent - JSON() deliberately doesn't
// include it, since the right relative path depends on where the file
// ends up, not on anything the builder knows.
package main

import (
	"fmt"
	"os"

	ab "axon-engine/agentbuilder"
)

func main() {
	convert := ab.Tool("convert", "pandoc_to_markdown", "{{user_input}}")

	fallback := ab.Tool("fallback", "echo", "(pandoc returned nothing for this input)")
	success := ab.Tool("success", "echo", ab.Output(convert))

	checkEmpty := ab.Conditional("check_empty", ab.Output(convert)+" == ", fallback, success, convert)

	agent := ab.New("html_to_markdown_safe", checkEmpty, convert, fallback, success).
		WithDescription("Converts HTML to Markdown via pandoc_to_markdown (#75), falling back to a clear message instead of silently returning empty output if pandoc produced nothing. Built with engine/agentbuilder (#56) as a real example of the typed builder workflow, not a hand-rebuilt copy of an existing agent. Requires pandoc to be installed on the worker's PATH.").
		WithOutputStep(success)

	data, err := agent.JSON()
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(data); err != nil {
		fmt.Fprintln(os.Stderr, "error writing output:", err)
		os.Exit(1)
	}
}
