package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	cli "axon-cli"
)

const defaultEngineURL = "http://localhost:8000"

func main() {
	if len(os.Args) < 2 {
		printUsage()
		os.Exit(1)
	}

	ctx := context.Background()

	switch os.Args[1] {
	case "run":
		runCommand(ctx, os.Args[2:])
	case "status":
		statusCommand(ctx, os.Args[2:])
	default:
		printUsage()
		os.Exit(1)
	}
}

// engineURL resolves the engine's base URL for a subcommand: --engine
// takes precedence, then AXON_ENGINE_URL, then localhost for local dev.
// A flag is provided alongside the env var since a URL that differs
// per invocation (e.g. scripting against multiple environments) is
// awkward to express as an env var alone.
func engineURL(fs *flag.FlagSet) string {
	flagValue := fs.Lookup("engine").Value.String()
	if flagValue != "" {
		return flagValue
	}
	if envValue := os.Getenv("AXON_ENGINE_URL"); envValue != "" {
		return envValue
	}
	return defaultEngineURL
}

func runCommand(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("run", flag.ExitOnError)
	fs.String("engine", "", "Engine API base URL (default: $AXON_ENGINE_URL, or "+defaultEngineURL+")")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, `usage: axon run [--engine URL] <agent_name> "<input>"`)
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 2 {
		fs.Usage()
		os.Exit(1)
	}
	agentName, input := fs.Arg(0), fs.Arg(1)

	client := cli.NewClient(engineURL(fs))
	run, err := client.CreateRunByName(ctx, agentName, input)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Run created: %s\n", run.ID)
}

func statusCommand(ctx context.Context, args []string) {
	fs := flag.NewFlagSet("status", flag.ExitOnError)
	fs.String("engine", "", "Engine API base URL (default: $AXON_ENGINE_URL, or "+defaultEngineURL+")")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: axon status [--engine URL] <run_id>")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		os.Exit(1)
	}
	if fs.NArg() < 1 {
		fs.Usage()
		os.Exit(1)
	}
	runID := fs.Arg(0)

	client := cli.NewClient(engineURL(fs))
	run, err := client.GetRun(ctx, runID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Print(cli.FormatRunStatus(run))
}

func printUsage() {
	fmt.Fprintln(os.Stderr, `axon - CLI client for the Axon orchestrator

Usage:
  axon run [--engine URL] <agent_name> "<input>"    Start a run for a registered agent
  axon status [--engine URL] <run_id>               Show a run's current status and result

Environment:
  AXON_ENGINE_URL   Engine API base URL (default: http://localhost:8000), overridden by --engine`)
}
