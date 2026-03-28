package acp

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// OpenCodeSession wraps an opencode ACP HTTP server.
//
// opencode ACP protocol (REST + SSE):
//
//	POST /session                          → create session, returns {id, ...}
//	POST /session/{id}/message             → send user message
//	GET  /global/event                     → SSE stream of all events
//
// Relevant SSE event types:
//
//	message.part.updated  → text chunk from the assistant
//	session.status        → session state change (used to detect completion)
type OpenCodeSession struct {
	cmd       *exec.Cmd
	port      int
	baseURL   string
	sessionID string
	workspace string
	done      chan struct{}
	closeOnce sync.Once
	client    *http.Client
}

// ocSSEEvent is the outer SSE data envelope from opencode.
// Actual JSON: {"directory":"...","payload":{"type":"event.name","properties":{...}}}
type ocSSEEvent struct {
	Payload struct {
		Type       string          `json:"type"`
		Properties json.RawMessage `json:"properties"`
	} `json:"payload"`
}

// ocPartProps is payload.properties for message.part.updated
type ocPartProps struct {
	SessionID string `json:"sessionID"`
	Part      struct {
		ID      string `json:"id"`
		Type    string `json:"type"`
		Text    string `json:"text"`
		Partial bool   `json:"partial"`
	} `json:"part"`
}

// ocStatusProps is payload.properties for session.status
type ocStatusProps struct {
	SessionID string `json:"sessionID"`
	Status    struct {
		Type string `json:"type"` // "idle" | "busy" | "error"
	} `json:"status"`
}

// ocErrorProps is payload.properties for session.error
type ocErrorProps struct {
	SessionID string `json:"sessionID"`
	Error     struct {
		Name string `json:"name"`
		Data struct {
			Message string `json:"message"`
		} `json:"data"`
	} `json:"error"`
}

// NewOpenCodeSession starts opencode acp and returns a ready session.
func NewOpenCodeSession(command string, args []string, workspace string) (*OpenCodeSession, error) {
	// Expand ~ in workspace path
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

	// Find a free port.
	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("failed to find free port: %w", err)
	}

	s := &OpenCodeSession{
		port:      port,
		baseURL:   fmt.Sprintf("http://127.0.0.1:%d", port),
		workspace: workspace,
		done:      make(chan struct{}),
		client:    &http.Client{Timeout: 0}, // no timeout for SSE
	}

	if err := s.startProcess(command, args); err != nil {
		return nil, fmt.Errorf("failed to start opencode: %w", err)
	}

	if err := s.waitReady(30 * time.Second); err != nil {
		s.Close()
		return nil, fmt.Errorf("opencode acp not ready: %w", err)
	}

	if err := s.createSession(); err != nil {
		s.Close()
		return nil, fmt.Errorf("failed to create opencode session: %w", err)
	}

	return s, nil
}

func (s *OpenCodeSession) startProcess(command string, args []string) error {
	// Build: <command> serve --port <N> [extra args...]
	// "opencode serve" is the headless mode; it doesn't require a TTY.
	// The working directory is set via cmd.Dir so --cwd is not needed.
	allArgs := []string{"serve", "--port", fmt.Sprintf("%d", s.port)}
	allArgs = append(allArgs, args...)

	if runtime.GOOS == "windows" {
		if strings.HasSuffix(command, ".cmd") || strings.HasSuffix(command, ".bat") {
			allArgs = append([]string{"/c", command}, allArgs...)
			command = "cmd"
		}
	}

	s.cmd = exec.Command(command, allArgs...)
	s.cmd.Stdin = nil // explicitly no stdin — prevents SIGTTIN when backgrounded
	s.cmd.Stdout = nil
	s.cmd.Stderr = nil
	if s.workspace != "" {
		s.cmd.Dir = s.workspace
	}

	if err := s.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start command %q: %w", command, err)
	}

	// Reap child when done to avoid zombies.
	go func() {
		s.cmd.Wait()
		s.closeOnce.Do(func() { close(s.done) })
	}()

	return nil
}

func (s *OpenCodeSession) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.done:
			return fmt.Errorf("process exited before becoming ready")
		default:
		}
		resp, err := s.client.Get(s.baseURL + "/session")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == 200 {
				return nil
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	return fmt.Errorf("timed out after %s", timeout)
}

func (s *OpenCodeSession) createSession() error {
	cwd := s.workspace
	if cwd == "" {
		cwd, _ = os.Getwd()
	}
	body := fmt.Sprintf(`{"projectPath":%q}`, cwd)
	resp, err := s.post("/session", body)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 {
		return fmt.Errorf("POST /session returned %d: %s", resp.StatusCode, raw)
	}

	var r struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &r); err != nil {
		return fmt.Errorf("failed to parse session response: %w", err)
	}
	if r.ID == "" {
		return fmt.Errorf("empty session ID in response: %s", raw)
	}
	s.sessionID = r.ID
	return nil
}

// Prompt sends a prompt and returns the assembled response.
func (s *OpenCodeSession) Prompt(ctx context.Context, prompt string, onChunk func(string)) (string, error) {
	if s.sessionID == "" {
		return "", fmt.Errorf("opencode session not initialized")
	}

	// Subscribe to SSE before sending the message to avoid missing early events.
	evCtx, stopEvents := context.WithCancel(ctx)
	defer stopEvents()

	parts, errCh := s.collectSSE(evCtx)

	// Send the user message.
	msgBody := fmt.Sprintf(`{"role":"user","parts":[{"type":"text","text":%s}]}`,
		jsonString(prompt))
	resp, err := s.postWithContext(ctx, "/session/"+s.sessionID+"/message", msgBody)
	if err != nil {
		return "", fmt.Errorf("failed to send message: %w", err)
	}
	resp.Body.Close()

	// Collect text chunks until the session goes idle or context done.
	var result strings.Builder
	for {
		select {
		case <-ctx.Done():
			return result.String(), ctx.Err()
		case err := <-errCh:
			return result.String(), err
		case chunk, ok := <-parts:
			if !ok {
				return result.String(), nil
			}
			result.WriteString(chunk)
			if onChunk != nil {
				onChunk(chunk)
			}
		}
	}
}

// collectSSE opens a /global/event SSE stream and returns:
//   - parts: channel of text chunks from message.part.updated events
//   - errCh: sends nil when idle (done) or an error
//
// The caller must cancel ctx to stop the goroutine.
func (s *OpenCodeSession) collectSSE(ctx context.Context) (<-chan string, <-chan error) {
	parts := make(chan string, 32)
	errCh := make(chan error, 1)

	go func() {
		defer close(parts)

		req, _ := http.NewRequestWithContext(ctx, "GET", s.baseURL+"/global/event", nil)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := s.client.Do(req)
		if err != nil {
			errCh <- err
			return
		}
		defer resp.Body.Close()

		scanner := bufio.NewScanner(resp.Body)
		var dataBuf bytes.Buffer

		// Track accumulated text per part ID so we can emit incremental chunks.
		partText := make(map[string]string)

		dispatchEvent := func() {
			data := bytes.TrimSpace(dataBuf.Bytes())
			dataBuf.Reset()
			if len(data) == 0 {
				return
			}

			var ev ocSSEEvent
			if err := json.Unmarshal(data, &ev); err != nil {
				return
			}
			evType := ev.Payload.Type
			props := ev.Payload.Properties

			switch evType {
			case "message.part.updated":
				var mp ocPartProps
				if err := json.Unmarshal(props, &mp); err == nil {
					if mp.Part.Type == "text" && mp.Part.Text != "" {
						prev := partText[mp.Part.ID]
						chunk := mp.Part.Text[len(prev):]
						if chunk != "" {
							partText[mp.Part.ID] = mp.Part.Text
							select {
							case parts <- chunk:
							case <-ctx.Done():
								return
							}
						}
					}
				}

			case "session.status":
				var ss ocStatusProps
				if err := json.Unmarshal(props, &ss); err == nil {
					if ss.Status.Type == "idle" {
						errCh <- nil
						return
					}
				}

			case "session.idle":
				// Alternative idle signal.
				errCh <- nil
				return

			case "session.error":
				var ep ocErrorProps
				if err := json.Unmarshal(props, &ep); err == nil {
					errCh <- fmt.Errorf("opencode error: %s: %s", ep.Error.Name, ep.Error.Data.Message)
				} else {
					errCh <- fmt.Errorf("opencode session error")
				}
				return
			}
		}

		for scanner.Scan() {
			line := scanner.Text()
			switch {
			case strings.HasPrefix(line, "data:"):
				dataBuf.WriteString(strings.TrimSpace(strings.TrimPrefix(line, "data:")))
			case line == "":
				dispatchEvent()
			}
		}
		if err := scanner.Err(); err != nil && err != io.EOF {
			errCh <- err
		}
	}()

	return parts, errCh
}

// Close stops the opencode process.
func (s *OpenCodeSession) Close() error {
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

// — helpers —

func (s *OpenCodeSession) post(path, body string) (*http.Response, error) {
	return s.postWithContext(context.Background(), path, body)
}

func (s *OpenCodeSession) postWithContext(ctx context.Context, path, body string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, "POST", s.baseURL+path,
		strings.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	return s.client.Do(req)
}

// freePort asks the OS for an unused TCP port.
func freePort() (int, error) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return port, nil
}

// jsonString returns a JSON-encoded string literal.
func jsonString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
