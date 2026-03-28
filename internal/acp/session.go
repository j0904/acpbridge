package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Session represents an ACP session
type Session struct {
	mu          sync.Mutex
	cmd         *exec.Cmd
	stdin       io.WriteCloser
	stdout      io.ReadCloser
	sessionID   string
	workspace   string
	requestID   int64
	done        chan struct{}
	notifications chan *Notification
	err         error
}

// NewSession creates a new ACP session
func NewSession(command string, args []string, workspace string) (*Session, error) {
	s := &Session{
		workspace:     workspace,
		done:          make(chan struct{}),
		notifications: make(chan *Notification, 100),
	}

	if err := s.startProcess(command, args); err != nil {
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	// Initialize ACP protocol
	if err := s.initialize(); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to initialize: %w", err)
	}

	// Create new session
	if err := s.createSession(); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to create session: %w", err)
	}

	// Start reading responses
	go s.readLoop()

	return s, nil
}

// startProcess starts the ACP CLI process
func (s *Session) startProcess(command string, args []string) error {
	// Build full command with --acp flag
	allArgs := append([]string{}, args...)
	allArgs = append(allArgs, "--acp")

	// Windows-specific handling for npm/npx
	if runtime.GOOS == "windows" {
		if strings.Contains(command, "npm") || strings.Contains(command, "npx") {
			// For npx/npm, build command as: cmd /c npx @package args...
			allArgs = append([]string{"/c", command}, allArgs...)
			command = "cmd"
		} else if strings.HasSuffix(command, ".cmd") || strings.HasSuffix(command, ".bat") {
			allArgs = append([]string{"/c", command}, allArgs...)
			command = "cmd"
		}
	}

	s.cmd = exec.Command(command, allArgs...)
	s.cmd.Env = append(s.cmd.Environ(),
		"NODE_OPTIONS=--max-old-space-size=4096",
		"QWEN_CODE_TELEMETRY=0",
	)

	// Set workspace as cwd if specified
	if s.workspace != "" {
		s.cmd.Dir = s.workspace
	}

	var err error
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	s.stdout, err = s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}

	// Capture stderr for debugging
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}
	go func() {
		scanner := bufio.NewScanner(stderr)
		for scanner.Scan() {
			// Log stderr if needed
			// fmt.Fprintf(os.Stderr, "[qwen-stderr] %s\n", scanner.Text())
		}
	}()

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command: %w", err)
	}

	return nil
}

// initialize performs ACP handshake
func (s *Session) initialize() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	params := map[string]interface{}{
		"protocolVersion": 1,
		"capabilities": map[string]interface{}{
			"roots": map[string]interface{}{
				"listChanged": true,
			},
		},
		"clientInfo": map[string]interface{}{
			"name":    "bridge-acp",
			"version": "0.1.0",
		},
	}

	var result InitializeResult
	if err := s.sendRequest(ctx, "initialize", params, &result); err != nil {
		return err
	}

	// Send initialized notification
	s.sendNotification("notifications/initialized", nil)

	return nil
}

// createSession creates a new ACP session
func (s *Session) createSession() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	params := map[string]interface{}{
		"cwd":        s.workspace,
		"mcpServers": []interface{}{},
	}

	var result SessionResult
	if err := s.sendRequest(ctx, "session/new", params, &result); err != nil {
		return err
	}

	s.sessionID = result.SessionID
	return nil
}

// Prompt sends a prompt to the ACP session and returns the response
func (s *Session) Prompt(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	s.mu.Lock()
	if s.sessionID == "" {
		s.mu.Unlock()
		return "", fmt.Errorf("session not initialized")
	}
	s.mu.Unlock()

	params := map[string]interface{}{
		"sessionId": s.sessionID,
		"prompt": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": prompt,
			},
		},
	}

	var parts []string
	var mu sync.Mutex

	// Set up chunk handler
	handler := func(chunk string) {
		mu.Lock()
		defer mu.Unlock()
		parts = append(parts, chunk)
		if onChunk != nil {
			onChunk(chunk)
		}
	}

	// Subscribe to notifications
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case notif := <-s.notifications:
				if notif.Method == "session/update" {
					var update Update
					if err := json.Unmarshal(notif.Params, &update); err != nil {
						continue
					}
					if update.Content.Type == "text" && update.Content.Text != "" {
						handler(update.Content.Text)
					}
				}
			case <-done:
				return
			}
		}
	}()

	// Send prompt request
	if err := s.sendRequest(ctx, "session/prompt", params, nil); err != nil {
		close(done)
		return "", err
	}

	// Wait a bit for final chunks
	select {
	case <-time.After(500 * time.Millisecond):
	case <-ctx.Done():
	}

	close(done)

	mu.Lock()
	defer mu.Unlock()
	return strings.Join(parts, ""), nil
}

// sendRequest sends a JSON-RPC request and waits for response
func (s *Session) sendRequest(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&s.requestID, 1)

	req := Envelope{
		ID:     json.RawMessage(fmt.Sprintf("%d", id)),
		Method: method,
		Params: mustMarshal(params),
	}

	if err := s.sendEnvelope(req); err != nil {
		return err
	}

	// Wait for response
	respChan := make(chan *Envelope, 1)
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-s.done:
				return
			case notif := <-s.notifications:
				_ = notif
			default:
				env, err := s.readEnvelope()
				if err != nil {
					return
				}
				if env.ID != nil && string(env.ID) == fmt.Sprintf("%d", id) {
					respChan <- env
					return
				}
			}
		}
	}()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case env := <-respChan:
		if env == nil {
			return fmt.Errorf("no response received")
		}
		if env.Error != nil {
			return fmt.Errorf("RPC error %d: %s", env.Error.Code, env.Error.Message)
		}
		if result != nil && env.Result != nil {
			return json.Unmarshal(env.Result, result)
		}
		return nil
	}
}

// sendNotification sends a JSON-RPC notification
func (s *Session) sendNotification(method string, params interface{}) error {
	req := Envelope{
		Method: method,
		Params: mustMarshal(params),
	}
	return s.sendEnvelope(req)
}

// sendEnvelope sends a JSON-RPC envelope
func (s *Session) sendEnvelope(env Envelope) error {
	data, err := json.Marshal(env)
	if err != nil {
		return err
	}
	_, err = s.stdin.Write(append(data, '\n'))
	return err
}

// readEnvelope reads a JSON-RPC envelope
func (s *Session) readEnvelope() (*Envelope, error) {
	reader := bufio.NewReader(s.stdout)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		return nil, err
	}

	var env Envelope
	if err := json.Unmarshal(bytes.TrimSpace(line), &env); err != nil {
		return nil, err
	}

	return &env, nil
}

// readLoop continuously reads from stdout
func (s *Session) readLoop() {
	reader := bufio.NewReader(s.stdout)
	for {
		select {
		case <-s.done:
			return
		default:
			line, err := reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					s.mu.Lock()
					s.err = err
					s.mu.Unlock()
				}
				close(s.done)
				return
			}

			var env Envelope
			if err := json.Unmarshal(bytes.TrimSpace(line), &env); err != nil {
				continue
			}

			// Handle notifications
			if env.ID == nil && env.Method != "" {
				s.notifications <- &Notification{
					Method: env.Method,
					Params: env.Params,
				}
			}
		}
	}
}

// Close closes the session
func (s *Session) Close() error {
	close(s.done)

	if s.stdin != nil {
		s.stdin.Close()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		if runtime.GOOS == "windows" {
			// Kill process tree on Windows
			exec.Command("taskkill", "/F", "/T", "/PID", fmt.Sprintf("%d", s.cmd.Process.Pid)).Run()
		} else {
			s.cmd.Process.Kill()
		}
	}

	return nil
}

func mustMarshal(v interface{}) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
