package api

import (
	engine "axon-engine"
	"github.com/gin-gonic/gin"
)

type Server struct {
	router       *gin.Engine
	orchestrator *engine.Orchestrator
}

func NewServer(orchestrator *engine.Orchestrator) *Server {
	router := gin.Default()
	server := &Server{
		router:       router,
		orchestrator: orchestrator,
	}

	// Define all routes
	server.setupRoutes()

	return server
}

func (s *Server) setupRoutes() {
	// define all HTTP endpoints
	s.router.POST("/webhook/complete", s.webhookCompleteHandler)
	s.router.GET("/runs/:id", s.getRunHandler)
	s.router.POST("/runs", s.createRunHandler)
}

// Start runs the HTTP server on the given port
func (s *Server) Start(port string) error {
	return s.router.Run(":" + port)
}
