package api

import (
	"axon-queue/internal/operations"
	"github.com/gin-gonic/gin"
)

// Server holds the Gin router and Queue instance
type Server struct {
	router *gin.Engine
	queue  *operations.Queue
}

// NewServer creates a new API server
func NewServer(queue *operations.Queue) *Server {
	router := gin.Default()
	server := &Server{
		router: router,
		queue:  queue,
	}

	// Define all routes
	server.setupRoutes()

	return server
}

// setupRoutes defines all HTTP endpoints
func (s *Server) setupRoutes() {
	// Health check
	s.router.GET("/health", s.healthHandler)

	// Stats
	s.router.GET("/stats", s.statsHandler)

	// Queue operations
	s.router.POST("/jobs", s.enqueueHandler)
	s.router.GET("/jobs/next", s.dequeueHandler)
	s.router.POST("/jobs/:id/ack", s.ackHandler)
	s.router.POST("/jobs/:id/fail", s.failHandler)
}

// Start runs the HTTP server on the given port
func (s *Server) Start(port string) error {
	return s.router.Run(":" + port)
}

