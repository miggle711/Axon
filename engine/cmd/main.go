package main

import (
	"flag"
	"log"

	engine "axon-engine"
	"axon-engine/api"
)

func main() {
	redisURL := flag.String("redis", "redis://localhost:6379", "Redis URL")
	queueURL := flag.String("queue", "http://localhost:8080", "Queue service URL")
	port := flag.String("port", "8000", "Port for the API server")
	flag.Parse()

	// Initialize the orchestrator with a Redis store and a queue client
	store, err := engine.NewRedisRunStore(*redisURL)
	if err != nil {
		log.Fatalf("Failed to connect to Redis: %v", err)
	}

	queueClient := engine.NewQueueClient(*queueURL)
	// No agent-loading mechanism exists yet, so the registry starts
	// empty; agent_call steps will fail with "unknown agent" until one
	// is registered here.
	agents := engine.MapAgentRegistry{}
	orchestrator := engine.NewOrchestrator(store, queueClient, agents)

	// Create the API server
	server := api.NewServer(orchestrator)

	// Start the API server
	log.Printf("Starting API server on port %s...", *port)
	if err := server.Start(*port); err != nil {
		log.Fatalf("Failed to start server: %v", err)
	}
}
