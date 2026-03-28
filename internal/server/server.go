package server

import (
	"net/http"
	"strings"
	"time"

	"bridge-acp/internal/acp"
	"bridge-acp/internal/config"
	"bridge-acp/pkg/types"

	"github.com/gin-gonic/gin"
)

// Server represents the HTTP server
type Server struct {
	cfg     *config.Config
	session *acp.Session
	engine  *gin.Engine
}

// New creates a new server
func New(cfg *config.Config) (*Server, error) {
	s := &Server{
		cfg: cfg,
	}

	// Initialize ACP session
	var err error
	s.session, err = acp.NewSession(cfg.CLI.Command, cfg.CLI.Args, cfg.CLI.Workspace)
	if err != nil {
		return nil, err
	}

	// Setup Gin
	gin.SetMode(gin.ReleaseMode)
	s.engine = gin.New()
	s.engine.Use(gin.Recovery())
	s.engine.Use(s.corsMiddleware())
	s.engine.Use(s.authMiddleware())
	s.engine.Use(gin.Logger())

	// Setup routes
	s.setupRoutes()

	return s, nil
}

// setupRoutes configures HTTP routes
func (s *Server) setupRoutes() {
	s.engine.GET("/health", s.handleHealth)
	s.engine.POST("/chat", s.handleChat)
	s.engine.POST("/chat/stream", s.handleChatStream)
	s.engine.POST("/v1/chat/completions", s.handleOpenAIChat)
}

// handleHealth handles health check requests
func (s *Server) handleHealth(c *gin.Context) {
	c.JSON(http.StatusOK, types.HealthResponse{Status: "ok"})
}

// handleChat handles synchronous chat requests
func (s *Server) handleChat(c *gin.Context) {
	var req types.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build prompt from messages
	prompt := buildPrompt(req.Messages, req.Message)

	// Send to ACP
	ctx := c.Request.Context()
	response, err := s.session.Prompt(ctx, prompt, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.ChatResponse{
		Reply:  response,
		Tokens: len([]rune(response)) / 4, // Rough estimate
		Model:  s.cfg.Model,
	})
}

// handleChatStream handles streaming chat requests with SSE
func (s *Server) handleChatStream(c *gin.Context) {
	var req types.ChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Setup SSE headers
	c.Header("Content-Type", "text/event-stream")
	c.Header("Cache-Control", "no-cache")
	c.Header("Connection", "keep-alive")
	c.Header("X-Accel-Buffering", "no")

	// Build prompt
	prompt := buildPrompt(req.Messages, req.Message)

	// Collect chunks
	var chunks []string
	var mu syncMutex

	// Send to ACP with chunk handler
	ctx := c.Request.Context()
	_, err := s.session.Prompt(ctx, prompt, func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		chunks = append(chunks, chunk)
		c.SSEvent("message", types.StreamChunk{Chunk: chunk})
		c.Writer.Flush()
	})

	if err != nil {
		c.SSEvent("error", gin.H{"error": err.Error()})
		return
	}

	// Send done signal
	c.SSEvent("message", types.StreamChunk{Done: true})
}

// handleOpenAIChat handles OpenAI-compatible chat requests
func (s *Server) handleOpenAIChat(c *gin.Context) {
	var req types.OpenAIChatRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
		return
	}

	// Build prompt from OpenAI messages
	prompt := buildPrompt(req.Messages, "")

	if req.Stream {
		// Streaming response
		c.Header("Content-Type", "text/event-stream")
		c.Header("Cache-Control", "no-cache")
		c.Header("Connection", "keep-alive")

		var content string
		var mu syncMutex

		ctx := c.Request.Context()
		_, err := s.session.Prompt(ctx, prompt, func(chunk string) {
			mu.Lock()
			defer mu.Unlock()
			content += chunk

			c.SSEvent("data", types.OpenAIStreamResponse{
				ID:      "chatcmpl-" + time.Now().Format("20060102150405"),
				Object:  "chat.completion.chunk",
				Created: time.Now().Unix(),
				Model:   req.Model,
				Choices: []types.StreamChoice{
					{
						Index: 0,
						Delta: types.Message{
							Role:    "assistant",
							Content: chunk,
						},
						FinishReason: "",
					},
				},
			})
			c.Writer.Flush()
		})

		if err != nil {
			c.SSEvent("data", gin.H{"error": err.Error()})
			return
		}

		// Send final chunk
		c.SSEvent("data", types.OpenAIStreamResponse{
			ID:      "chatcmpl-" + time.Now().Format("20060102150405"),
			Object:  "chat.completion.chunk",
			Created: time.Now().Unix(),
			Model:   req.Model,
			Choices: []types.StreamChoice{
				{
					Index:        0,
					Delta:        types.Message{},
					FinishReason: "stop",
				},
			},
		})

		return
	}

	// Synchronous response
	ctx := c.Request.Context()
	response, err := s.session.Prompt(ctx, prompt, nil)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{"error": err.Error()})
		return
	}

	c.JSON(http.StatusOK, types.OpenAIChatResponse{
		ID:      "chatcmpl-" + time.Now().Format("20060102150405"),
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []types.Choice{
			{
				Index: 0,
				Message: types.Message{
					Role:    "assistant",
					Content: response,
				},
				FinishReason: "stop",
			},
		},
		Usage: &types.Usage{
			PromptTokens:     len([]rune(prompt)) / 4,
			CompletionTokens: len([]rune(response)) / 4,
			TotalTokens:      (len([]rune(prompt)) + len([]rune(response))) / 4,
		},
	})
}

// buildPrompt builds a prompt from messages
func buildPrompt(messages []types.Message, singleMessage string) string {
	var sb strings.Builder

	if singleMessage != "" {
		return singleMessage
	}

	for _, msg := range messages {
		switch msg.Role {
		case "system":
			sb.WriteString("System: ")
		case "user":
			sb.WriteString("User: ")
		case "assistant":
			sb.WriteString("Assistant: ")
		}
		sb.WriteString(msg.Content)
		sb.WriteString("\n")
	}

	return sb.String()
}

// corsMiddleware handles CORS
func (s *Server) corsMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Header("Access-Control-Allow-Origin", "*")
		c.Header("Access-Control-Allow-Methods", "GET, POST, OPTIONS")
		c.Header("Access-Control-Allow-Headers", "Origin, Content-Type, Authorization")

		if c.Request.Method == "OPTIONS" {
			c.AbortWithStatus(http.StatusNoContent)
			return
		}

		c.Next()
	}
}

// authMiddleware handles API key authentication
func (s *Server) authMiddleware() gin.HandlerFunc {
	return func(c *gin.Context) {
		if s.cfg.APIKey == "" {
			c.Next()
			return
		}

		auth := c.GetHeader("Authorization")
		if strings.HasPrefix(auth, "Bearer ") {
			token := strings.TrimPrefix(auth, "Bearer ")
			if token != s.cfg.APIKey {
				c.JSON(http.StatusUnauthorized, gin.H{"error": "invalid API key"})
				c.Abort()
				return
			}
		}

		c.Next()
	}
}

// syncMutex is a simple mutex wrapper
type syncMutex struct {
	ch chan struct{}
}

func (m *syncMutex) Lock() {
	if m.ch == nil {
		m.ch = make(chan struct{}, 1)
	}
	m.ch <- struct{}{}
}

func (m *syncMutex) Unlock() {
	if m.ch != nil {
		<-m.ch
	}
}

// Run starts the HTTP server
func (s *Server) Run() error {
	return s.engine.Run(s.cfg.Listen)
}

// Close closes the server
func (s *Server) Close() error {
	if s.session != nil {
		return s.session.Close()
	}
	return nil
}
