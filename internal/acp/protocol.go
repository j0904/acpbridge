package acp

import "encoding/json"

// Envelope represents an ACP JSON-RPC envelope
type Envelope struct {
	ID     json.RawMessage `json:"id,omitempty"`
	Method string          `json:"method,omitempty"`
	Params json.RawMessage `json:"params,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *ErrorData      `json:"error,omitempty"`
}

// ErrorData represents JSON-RPC error data
type ErrorData struct {
	Code    int             `json:"code"`
	Message string          `json:"message"`
	Data    json.RawMessage `json:"data,omitempty"`
}

// Notification represents an ACP notification
type Notification struct {
	Method string          `json:"method"`
	Params json.RawMessage `json:"params"`
}

// Update represents a session update
type Update struct {
	ID      string      `json:"id"`
	Content Content     `json:"content"`
	Extra   interface{} `json:"extra,omitempty"`
}

// Content represents message content
type Content struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// SessionResult represents session/new result
type SessionResult struct {
	SessionID string `json:"sessionId"`
}

// InitializeResult represents initialize result
type InitializeResult struct {
	ProtocolVersion int `json:"protocolVersion"`
	AgentInfo       struct {
		Name    string `json:"name"`
		Title   string `json:"title"`
		Version string `json:"version"`
	} `json:"agentInfo"`
	AuthMethods []struct {
		ID          string `json:"id"`
		Name        string `json:"name"`
		Description string `json:"description"`
	} `json:"authMethods"`
	AgentCapabilities struct {
		LoadSession bool `json:"loadSession"`
		Prompt      struct {
			Image           bool `json:"image"`
			Audio           bool `json:"audio"`
			EmbeddedContext bool `json:"embeddedContext"`
		} `json:"promptCapabilities"`
		Session struct {
			List  map[string]interface{} `json:"list"`
			Resume map[string]interface{} `json:"resume"`
		} `json:"sessionCapabilities"`
	} `json:"agentCapabilities"`
}
