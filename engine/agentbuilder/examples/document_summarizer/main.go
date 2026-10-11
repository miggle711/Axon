// Command document_summarizer builds a genuinely new, non-mirror
// agent: converts an HTML document to markdown via pandoc_to_markdown
// (#75), then summarizes it with an llm_call. pandoc_to_markdown has
// existed since #75 but nothing besides html_to_markdown_safe.json has
// ever used it, and that agent only converts, it never does anything
// with the result - this is the first real task built on top of it.
//
// Run from engine/ with:
//
//	go run ./agentbuilder/examples/document_summarizer
package main

import (
	ab "axon-engine/agentbuilder"
	"fmt"
	"os"
)

func main() {
	convert := ab.Tool("convert", "pandoc_to_markdown", "{{user_input}}")
	summarize := ab.LLM("summarize",
		"Summarize the document below in a few clear sentences.\n\nDocument:\n"+ab.Output(convert),
		convert,
	)

	agent := ab.New("document_summarizer", convert, summarize).
		WithDescription("Converts an HTML document to markdown via pandoc_to_markdown, then summarizes it with an llm_call. Requires pandoc on the worker's PATH and GROQ_API_KEY for the summarize step.").
		WithOutputStep(summarize)

	data, err := agent.JSON()
	if err != nil {
		fail(err)
	}
	withSchema := append([]byte("{\n  \"$schema\": \"./agent.schema.json\",\n"), data[2:]...)
	if err := os.WriteFile("agents/document_summarizer.json", withSchema, 0644); err != nil {
		fail(err)
	}
	fmt.Println("Wrote engine/agents/document_summarizer.json")
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
