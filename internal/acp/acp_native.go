package acp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

type acpMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      *int            `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *acpRPCError    `json:"error,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type acpRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

type acpNotification struct {
	SessionID string          `json:"sessionId"`
	Update    json.RawMessage `json:"update"`
}

type acpUpdateBody struct {
	SessionUpdate string          `json:"sessionUpdate"`
	Content       json.RawMessage `json:"content,omitempty"`
	Status        string          `json:"status,omitempty"`
	ToolCallID    string          `json:"toolCallId,omitempty"`
	ToolName      string          `json:"toolName,omitempty"`
}

// AcpNativeSession communicates with opencode via JSON-RPC 2.0 over
// stdin/stdout (ACP protocol).  This replaces the REST+SSE approach and is
// stable because pipes are tied to the process lifetime.
type AcpNativeSession struct {
	cmd    *exec.Cmd
	stdin  io.WriteCloser
	stdout *bufio.Scanner

	sessionID string
	workspace string
	model     string

	done      chan struct{}
	closeOnce sync.Once

	mu     sync.Mutex
	nextID int32

	updates   chan *acpMessage
	pending   map[int]chan *acpMessage
	pendingMu sync.Mutex
}

func NewAcpNativeSession(command string, args []string, workspace string, model string) (*AcpNativeSession, error) {
	if strings.HasPrefix(workspace, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			workspace = filepath.Join(home, workspace[2:])
		}
	}
	if workspace != "" {
		if err := os.MkdirAll(workspace, 0750); err != nil {
			return nil, fmt.Errorf("failed to create workspace: %w", err)
		}
	}

	acpArgs := []string{"acp"}
	acpArgs = append(acpArgs, args...)

	if runtime.GOOS == "windows" {
		if strings.HasSuffix(command, ".cmd") || strings.HasSuffix(command, ".bat") {
			acpArgs = append([]string{"/c", command}, acpArgs...)
			command = "cmd"
		}
	}

	cmd := exec.Command(command, acpArgs...)
	cmd.Stderr = nil
	if workspace != "" {
		cmd.Dir = workspace
	}

	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("stdin pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %q: %w", command, err)
	}

	s := &AcpNativeSession{
		cmd:       cmd,
		stdin:     stdin,
		stdout:    bufio.NewScanner(stdout),
		workspace: workspace,
		model:     model,
		done:      make(chan struct{}),
		updates:   make(chan *acpMessage, 64),
		pending:   make(map[int]chan *acpMessage),
	}

	go func() {
		cmd.Wait()
		s.closeOnce.Do(func() { close(s.done) })
	}()

	go s.readLoop()

	if err := s.initialize(); err != nil {
		s.Close()
		return nil, fmt.Errorf("acp initialize: %w", err)
	}

	if err := s.newSession(); err != nil {
		s.Close()
		return nil, fmt.Errorf("acp newSession: %w", err)
	}

	// Pre-warm the session so the first real request is fast.
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		s.Prompt(ctx, "ping", nil)
	}()

	return s, nil
}

func (s *AcpNativeSession) Prompt(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	if s.sessionID == "" {
		return "", fmt.Errorf("acp session not initialized")
	}

	reqID := s.nextRequestID()
	req := struct {
		SessionID string    `json:"sessionId"`
		Prompt    []acpPart `json:"prompt"`
	}{
		SessionID: s.sessionID,
		Prompt:    []acpPart{{Type: "text", Text: prompt}},
	}

	respCh := make(chan *acpMessage, 1)
	s.pendingMu.Lock()
	s.pending[reqID] = respCh
	s.pendingMu.Unlock()

	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, reqID)
		s.pendingMu.Unlock()
	}()

	if err := s.sendRequest(reqID, "session/prompt", req); err != nil {
		return "", fmt.Errorf("send prompt: %w", err)
	}

	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case <-s.done:
		return "", fmt.Errorf("opencode process exited")
	case resp := <-respCh:
		if resp.Error != nil {
			return "", fmt.Errorf("prompt error: %s (code %d)", resp.Error.Message, resp.Error.Code)
		}
	}

	var result strings.Builder
	for {
		select {
		case msg, ok := <-s.updates:
			if !ok {
				return result.String(), nil
			}
			s.collectChunk(msg, &result, onChunk)
		default:
			return result.String(), nil
		}
	}
}

func (s *AcpNativeSession) collectChunk(msg *acpMessage, result *strings.Builder, onChunk func(string)) {
	var notif acpNotification
	if err := json.Unmarshal(msg.Params, &notif); err != nil {
		return
	}
	var upd acpUpdateBody
	if err := json.Unmarshal(notif.Update, &upd); err != nil {
		return
	}
	if upd.SessionUpdate != "agent_message_chunk" && upd.SessionUpdate != "agent_thought_chunk" {
		return
	}
	var content struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(upd.Content, &content); err != nil || content.Text == "" {
		return
	}
	result.WriteString(content.Text)
	if onChunk != nil {
		onChunk(content.Text)
	}
}

func (s *AcpNativeSession) Close() error {
	s.closeOnce.Do(func() { close(s.done) })
	if s.cmd != nil && s.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			exec.Command("taskkill", "/F", "/T", "/PID",
				fmt.Sprintf("%d", s.cmd.Process.Pid)).Run()
		} else {
			s.cmd.Process.Kill()
		}
	}
	return nil
}

type acpPart struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

func (s *AcpNativeSession) initialize() error {
	params := map[string]interface{}{
		"protocolVersion": 1,
		"clientCapabilities": map[string]interface{}{
			"fs": map[string]bool{"readTextFile": false, "writeTextFile": false},
		},
		"clientInfo": map[string]string{
			"name": "bridge-acp", "version": "0.2.0",
		},
	}

	reqID := s.nextRequestID()
	respCh := make(chan *acpMessage, 1)
	s.pendingMu.Lock()
	s.pending[reqID] = respCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, reqID)
		s.pendingMu.Unlock()
	}()

	if err := s.sendRequest(reqID, "initialize", params); err != nil {
		return err
	}

	select {
	case <-s.done:
		return fmt.Errorf("opencode exited during initialize")
	case resp := <-respCh:
		if resp.Error != nil {
			return fmt.Errorf("initialize: %s (code %d)", resp.Error.Message, resp.Error.Code)
		}
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("initialize timed out after 30s")
	}
}

func (s *AcpNativeSession) newSession() error {
	cwd := s.workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	params := map[string]interface{}{
		"cwd":        cwd,
		"mcpServers": []interface{}{},
	}

	reqID := s.nextRequestID()
	respCh := make(chan *acpMessage, 1)
	s.pendingMu.Lock()
	s.pending[reqID] = respCh
	s.pendingMu.Unlock()
	defer func() {
		s.pendingMu.Lock()
		delete(s.pending, reqID)
		s.pendingMu.Unlock()
	}()

	if err := s.sendRequest(reqID, "session/new", params); err != nil {
		return err
	}

	select {
	case <-s.done:
		return fmt.Errorf("opencode exited during newSession")
	case resp := <-respCh:
		if resp.Error != nil {
			return fmt.Errorf("newSession: %s (code %d)", resp.Error.Message, resp.Error.Code)
		}
		var result struct {
			SessionID string `json:"sessionId"`
		}
		if err := json.Unmarshal(resp.Result, &result); err != nil {
			return fmt.Errorf("parse newSession response: %w", err)
		}
		if result.SessionID == "" {
			return fmt.Errorf("empty sessionId in response")
		}
		s.sessionID = result.SessionID
		return nil
	case <-time.After(30 * time.Second):
		return fmt.Errorf("newSession timed out after 30s")
	}
}

func (s *AcpNativeSession) nextRequestID() int {
	return int(atomic.AddInt32(&s.nextID, 1))
}

func (s *AcpNativeSession) sendRequest(id int, method string, params interface{}) error {
	msg := acpMessage{
		JSONRPC: "2.0",
		ID:      &id,
		Method:  method,
	}
	raw, err := json.Marshal(params)
	if err != nil {
		return err
	}
	msg.Params = raw

	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	data = append(data, '\n')

	s.mu.Lock()
	defer s.mu.Unlock()
	select {
	case <-s.done:
		return fmt.Errorf("session closed")
	default:
	}
	_, err = s.stdin.Write(data)
	return err
}

func (s *AcpNativeSession) readLoop() {
	defer close(s.updates)
	for s.stdout.Scan() {
		line := s.stdout.Bytes()
		if len(line) == 0 {
			continue
		}
		var msg acpMessage
		if err := json.Unmarshal(line, &msg); err != nil {
			continue
		}

		if msg.ID != nil {
			s.pendingMu.Lock()
			ch, ok := s.pending[*msg.ID]
			s.pendingMu.Unlock()
			if ok {
				select {
				case ch <- &msg:
				default:
				}
			}
		} else {
			select {
			case s.updates <- &msg:
			default:
			}
		}
	}
}
