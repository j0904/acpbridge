package types

// Message represents a chat message
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatRequest represents a chat request
type ChatRequest struct {
	Messages []Message          `json:"messages,omitempty"`
	Message  string             `json:"message,omitempty"`
	Model    string             `json:"model,omitempty"`
	Stream   bool               `json:"stream,omitempty"`
	Options  *ChatOptions       `json:"options,omitempty"`
}

// ChatOptions represents chat options
type ChatOptions struct {
	Temperature float64 `json:"temperature,omitempty"`
	MaxTokens   int     `json:"max_tokens,omitempty"`
}

// ChatResponse represents a chat response
type ChatResponse struct {
	Reply  string `json:"reply"`
	Tokens int    `json:"tokens,omitempty"`
	Model  string `json:"model,omitempty"`
}

// StreamChunk represents a streaming chunk
type StreamChunk struct {
	Chunk string `json:"chunk"`
	Done  bool   `json:"done,omitempty"`
}

// OpenAIChatRequest represents OpenAI-compatible chat request
type OpenAIChatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	Stream      bool      `json:"stream,omitempty"`
	Temperature float64   `json:"temperature,omitempty"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
}

// OpenAIChatResponse represents OpenAI-compatible chat response
type OpenAIChatResponse struct {
	ID      string   `json:"id"`
	Object  string   `json:"object"`
	Created int64    `json:"created"`
	Model   string   `json:"model"`
	Choices []Choice `json:"choices"`
	Usage   *Usage   `json:"usage,omitempty"`
}

// Choice represents a chat completion choice
type Choice struct {
	Index        int     `json:"index"`
	Message      Message `json:"message"`
	FinishReason string  `json:"finish_reason"`
}

// Usage represents token usage
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// StreamChoice represents a streaming choice
type StreamChoice struct {
	Index        int     `json:"index"`
	Delta        Message `json:"delta"`
	FinishReason string  `json:"finish_reason,omitempty"`
}

// OpenAIStreamResponse represents OpenAI streaming response
type OpenAIStreamResponse struct {
	ID      string        `json:"id"`
	Object  string        `json:"object"`
	Created int64         `json:"created"`
	Model   string        `json:"model"`
	Choices []StreamChoice `json:"choices"`
}

// HealthResponse represents health check response
type HealthResponse struct {
	Status string `json:"status"`
}
