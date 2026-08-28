package main

import (
	"flag"
	"log/slog"
	"os"

	engine "axon-engine"
	"axon-engine/api"
)

func main() {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, nil))

	redisURL := flag.String("redis", "redis://localhost:6379", "Redis URL")
	queueURL := flag.String("queue", "http://localhost:8080", "Queue service URL")
	port := flag.String("port", "8000", "Port for the API server")
	agentsDir := flag.String("agents", "agents", "Directory of agent JSON files, for agent_call to resolve sub-agents by name")
	flag.Parse()

	// Initialize the orchestrator with a Redis store and a queue client
	store, err := engine.NewRedisRunStore(*redisURL)
	if err != nil {
		logger.Error("failed to connect to Redis", "error", err)
		os.Exit(1)
	}

	queueClient := engine.NewQueueClient(*queueURL)

	agents, err := engine.LoadAgentsFromDir(*agentsDir)
	if err != nil {
		logger.Error("failed to load agents", "dir", *agentsDir, "error", err)
		os.Exit(1)
	}
	logger.Info("loaded agents", "count", len(agents), "dir", *agentsDir)

	orchestrator := engine.NewOrchestrator(store, queueClient, agents, logger)

	// Create the API server
	server := api.NewServer(orchestrator)

	// Start the API server
	logger.Info("starting API server", "port", *port)
	if err := server.Start(*port); err != nil {
		logger.Error("failed to start server", "error", err)
		os.Exit(1)
	}
}
