package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

// Session represents an ACP session
type Session struct {
	mu            sync.Mutex
	restartMu     sync.Mutex
	cmd           *exec.Cmd
	stdin         io.WriteCloser
	reader        *bufio.Reader
	sessionID     string
	command       string
	args          []string
	workspace     string
	requestID     int64
	done          chan struct{}
	closeOnce     sync.Once
	notifications chan *Notification
	pending       map[int64]chan *Envelope
	err           error
}

// NewSession creates a new ACP session
func NewSession(command string, args []string, workspace string) (*Session, error) {
	s := &Session{
		workspace:     workspace,
		command:       command,
		args:          args,
		done:          make(chan struct{}),
		notifications: make(chan *Notification, 100),
		pending:       make(map[int64]chan *Envelope),
	}

	if err := s.startProcess(command, args); err != nil {
		return nil, fmt.Errorf("failed to start process: %w", err)
	}

	// Start reading responses before handshake so sendRequest can work
	go s.readLoop()

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

	return s, nil
}

// startProcess starts the ACP CLI process
func (s *Session) startProcess(command string, args []string) error {
	// Expand ~ in workspace path
	workspace := s.workspace
	if strings.HasPrefix(workspace, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			workspace = filepath.Join(home, workspace[2:])
		}
	}
	s.workspace = workspace

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

	// Set workspace as cwd if specified; create it if needed
	if s.workspace != "" {
		if err := os.MkdirAll(s.workspace, 0750); err == nil {
			s.cmd.Dir = s.workspace
		}
	}

	var err error
	s.stdin, err = s.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdin pipe: %w", err)
	}

	stdout, err := s.cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("failed to get stdout pipe: %w", err)
	}
	s.reader = bufio.NewReader(stdout)

	// Capture stderr for debugging
	stderr, err := s.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to get stderr pipe: %w", err)
	}
	// Use io.Copy instead of bufio.Scanner to avoid 64KB line limit
	// that causes silent failure and pipe backpressure.
	go func() {
		_, _ = io.Copy(io.Discard, stderr)
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

// Prompt sends a prompt to the ACP session and returns the response.
// If the subprocess is unresponsive, it will be automatically restarted.
func (s *Session) Prompt(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	s.mu.Lock()
	if s.sessionID == "" {
		s.mu.Unlock()
		return "", fmt.Errorf("session not initialized")
	}
	s.mu.Unlock()

	return s.doPrompt(ctx, prompt, onChunk, false)
}

// doPrompt performs the prompt with auto-restart on failure.
func (s *Session) doPrompt(ctx context.Context, prompt string, onChunk func(string), retried bool) (string, error) {
	result, err := s.doPromptOnce(ctx, prompt, onChunk)
	if err != nil {
		// If we haven't retried yet and the error suggests process failure, restart.
		if !retried && shouldRestartOnError(err) {
			fmt.Fprintf(os.Stderr, "[bridge-acp] qwen process unresponsive, restarting...\n")
			if restartErr := s.Restart(); restartErr != nil {
				return "", fmt.Errorf("failed to restart process: %w (original: %v)", restartErr, err)
			}
			fmt.Fprintf(os.Stderr, "[bridge-acp] qwen process restarted successfully\n")

			// If the request already hit its deadline, fail this call but keep the
			// bridge healthy for the next request.
			if errors.Is(err, context.DeadlineExceeded) {
				return "", err
			}

			// Retry once for transport-style failures (EOF/broken pipe/session closed).
			return s.doPrompt(ctx, prompt, onChunk, true)
		}
	}
	return result, err
}

func shouldRestartOnError(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	msg := err.Error()
	return strings.Contains(msg, "session closed") ||
		strings.Contains(msg, "EOF") ||
		strings.Contains(msg, "broken pipe")
}

// doPromptOnce performs a single prompt attempt without restart logic.
func (s *Session) doPromptOnce(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	s.mu.Lock()
	sessionID := s.sessionID
	s.mu.Unlock()

	params := map[string]interface{}{
		"sessionId": sessionID,
		"prompt": []interface{}{
			map[string]interface{}{
				"type": "text",
				"text": prompt,
			},
		},
	}

	var parts []string
	var partsMu sync.Mutex

	handleNotif := func(notif *Notification) {
		if notif.Method == "session/update" {
			var update Update
			if err := json.Unmarshal(notif.Params, &update); err != nil {
				return
			}
			// Only capture agent message chunks (not thought chunks or other updates).
			if update.Update.SessionUpdate == "agent_message_chunk" &&
				update.Update.Content.Type == "text" &&
				update.Update.Content.Text != "" {
				partsMu.Lock()
				parts = append(parts, update.Update.Content.Text)
				partsMu.Unlock()
				if onChunk != nil {
					onChunk(update.Update.Content.Text)
				}
			}
		}
	}

	// Collect session/update notifications while the request is in flight.
	collectCtx, stopCollect := context.WithCancel(ctx)
	collectorDone := make(chan struct{})
	go func() {
		defer close(collectorDone)
		for {
			select {
			case <-collectCtx.Done():
				// Drain any buffered notifications before exiting.
				for {
					select {
					case notif := <-s.notifications:
						handleNotif(notif)
					default:
						return
					}
				}
			case <-s.done:
				return
			case notif := <-s.notifications:
				handleNotif(notif)
			}
		}
	}()

	// Send prompt request; blocks until the RPC response is received.
	err := s.sendRequest(ctx, "session/prompt", params, nil)

	// Allow trailing notifications a brief window to arrive before draining.
	time.Sleep(200 * time.Millisecond)
	stopCollect()
	<-collectorDone

	partsMu.Lock()
	result := strings.Join(parts, "")
	partsMu.Unlock()
	return result, err
}

// Restart kills the current subprocess and starts a new one with full re-initialization.
func (s *Session) Restart() error {
	s.restartMu.Lock()
	defer s.restartMu.Unlock()

	// Snapshot current process handles under lock.
	s.mu.Lock()
	oldCmd := s.cmd
	oldStdin := s.stdin
	s.mu.Unlock()

	// Stop current process outside lock to avoid blocking other paths.
	s.closeOnce.Do(func() { close(s.done) })
	if oldStdin != nil {
		_ = oldStdin.Close()
	}
	if oldCmd != nil && oldCmd.Process != nil {
		_ = oldCmd.Process.Kill()
		_, _ = oldCmd.Process.Wait()
	}

	// Reset mutable state under lock.
	s.mu.Lock()
	s.done = make(chan struct{})
	s.closeOnce = sync.Once{}
	s.sessionID = ""
	s.requestID = 0
	s.pending = make(map[int64]chan *Envelope)
	s.notifications = make(chan *Notification, 100)
	s.err = nil
	s.mu.Unlock()

	// Start and initialize fresh process without holding s.mu.
	if err := s.startProcess(s.command, s.args); err != nil {
		return fmt.Errorf("failed to start process: %w", err)
	}

	go s.readLoop()

	if err := s.initialize(); err != nil {
		s.Close()
		return fmt.Errorf("failed to initialize: %w", err)
	}

	if err := s.createSession(); err != nil {
		s.Close()
		return fmt.Errorf("failed to create session: %w", err)
	}

	return nil
}

// sendRequest sends a JSON-RPC request and waits for the response via readLoop.
func (s *Session) sendRequest(ctx context.Context, method string, params interface{}, result interface{}) error {
	id := atomic.AddInt64(&s.requestID, 1)

	respChan := make(chan *Envelope, 1)
	s.mu.Lock()
	s.pending[id] = respChan
	s.mu.Unlock()

	req := Envelope{
		ID:     json.RawMessage(fmt.Sprintf("%d", id)),
		Method: method,
		Params: mustMarshal(params),
	}

	if err := s.sendEnvelope(req); err != nil {
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return err
	}

	select {
	case <-ctx.Done():
		s.mu.Lock()
		delete(s.pending, id)
		s.mu.Unlock()
		return ctx.Err()
	case <-s.done:
		return fmt.Errorf("session closed")
	case env := <-respChan:
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

// readLoop is the sole reader of stdout; it dispatches responses and notifications.
func (s *Session) readLoop() {
	for {
		select {
		case <-s.done:
			return
		default:
			line, err := s.reader.ReadBytes('\n')
			if err != nil {
				if err != io.EOF {
					s.mu.Lock()
					s.err = err
					s.mu.Unlock()
				}
				s.closeOnce.Do(func() { close(s.done) })
				return
			}

			var env Envelope
			if err := json.Unmarshal(bytes.TrimSpace(line), &env); err != nil {
				continue
			}

			if env.ID != nil {
				// It's a response — dispatch to waiting sendRequest caller.
				var id int64
				if err := json.Unmarshal(env.ID, &id); err == nil {
					s.mu.Lock()
					ch, ok := s.pending[id]
					if ok {
						delete(s.pending, id)
					}
					s.mu.Unlock()
					if ok {
						ch <- &env
					}
				}
			} else if env.Method != "" {
				// It's a notification.
				select {
				case s.notifications <- &Notification{Method: env.Method, Params: env.Params}:
				default:
					// Drop if buffer full to avoid blocking readLoop.
				}
			}
		}
	}
}

// Close closes the session
func (s *Session) Close() error {
	s.closeOnce.Do(func() { close(s.done) })

	if s.stdin != nil {
		s.stdin.Close()
	}

	if s.cmd != nil && s.cmd.Process != nil {
		if runtime.GOOS == "windows" {
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
