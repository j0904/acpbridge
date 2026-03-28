# bridge-acp

A standalone HTTP bridge server that wraps AI coding CLI tools (Qwen Code or OpenCode) via ACP (Agent Communication Protocol), exposing them as a unified HTTP API.

## Architecture

**Qwen driver** (JSON-RPC over stdio):
```
HTTP Client → bridge-acp (:9090) → qwen --acp (stdio) → Remote LLM
```

**OpenCode driver** (REST + SSE):
```
HTTP Client → bridge-acp (:9090) → opencode serve (HTTP) → Remote LLM
```

## Features

- **Two drivers**: Qwen Code (default) and OpenCode
- **HTTP API**: REST endpoints for sync/streaming chat
- **OpenAI Compatible**: `/v1/chat/completions` endpoint
- **SSE Streaming**: Real-time token streaming
- **Cross-platform**: Windows, macOS, Linux

## Quick Start

### 1. Install Go

Download from: https://go.dev/dl/

### 2. Build

```bash
go mod tidy
go build -o bridge-acp ./cmd/bridge-acp
```

### 3. Configure

Copy and edit `config.example.json`:

```bash
cp config.example.json config.json
```

**Qwen driver** (`config.json`):
```json
{
  "listen": ":9090",
  "api_key": "",
  "driver": "qwen",
  "cli": {
    "command": "qwen",
    "args": ["--approval-mode=yolo"],
    "workspace": "~/.picoclaw/qwen-ws"
  },
  "model": "qwen-cli/qwen-max"
}
```

**OpenCode driver** (`config.opencode.json`):
```json
{
  "listen": ":9090",
  "api_key": "",
  "driver": "opencode",
  "cli": {
    "command": "opencode",
    "args": [],
    "workspace": "~/my-workspace"
  },
  "model": "opencode"
}
```

> `driver` defaults to `"qwen"` when omitted.

### 4. Run

```bash
./bridge-acp --config config.json
```

## Testing

### Health check

```bash
# Verify the bridge is running
go run - <<'EOF'
package main
import ("fmt";"io";"net/http")
func main() {
    r, _ := http.Get("http://localhost:9090/health")
    b, _ := io.ReadAll(r.Body)
    fmt.Printf("%d %s\n", r.StatusCode, b)
}
EOF
```

Expected: `200 {"status":"ok"}`

### Sync chat

```go
package main

import (
    "context"
    "fmt"
    "io"
    "net/http"
    "strings"
    "time"
)

func main() {
    ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
    defer cancel()

    req, _ := http.NewRequestWithContext(ctx, "POST", "http://localhost:9090/chat",
        strings.NewReader(`{"message":"say hello in one word"}`))
    req.Header.Set("Content-Type", "application/json")

    resp, err := http.DefaultClient.Do(req)
    if err != nil { fmt.Println("error:", err); return }
    body, _ := io.ReadAll(resp.Body)
    resp.Body.Close()
    fmt.Printf("%d %s\n", resp.StatusCode, body)
}
```

Expected: `200 {"reply":"Hello","tokens":1,"model":"qwen-cli/qwen-max"}`

### Streaming chat (SSE)

```bash
# Using a Go test client or any SSE-capable client
POST http://localhost:9090/chat/stream
Content-Type: application/json
{"message": "count to 5"}
```

Events:
```
data: {"chunk":"1"}
data: {"chunk":", 2, 3, 4, 5"}
data: {"done":true}
```

### OpenAI-compatible endpoint

```bash
POST http://localhost:9090/v1/chat/completions
Content-Type: application/json

{
  "model": "qwen-cli/qwen-max",
  "messages": [{"role": "user", "content": "Hello"}]
}
```

## API Endpoints

### `GET /health`

```json
{"status": "ok"}
```

### `POST /chat`

Request:
```json
{"message": "Hello"}
```
or with message history:
```json
{"messages": [{"role": "user", "content": "Hello"}]}
```

Response:
```json
{"reply": "Hi there!", "tokens": 3, "model": "qwen-cli/qwen-max"}
```

### `POST /chat/stream`

Same request format as `/chat`. Returns SSE:
```
data: {"chunk":"Hi"}
data: {"chunk":" there!"}
data: {"done":true}
```

### `POST /v1/chat/completions`

OpenAI-compatible. Supports `"stream": true/false`.

## Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `listen` | string | `:9090` | HTTP listen address |
| `api_key` | string | `""` | Bearer token auth (empty = no auth) |
| `driver` | string | `qwen` | `"qwen"` or `"opencode"` |
| `cli.command` | string | `qwen` | CLI executable |
| `cli.args` | array | `["--approval-mode=yolo"]` | Extra CLI arguments |
| `cli.workspace` | string | `~/.picoclaw/qwen-ws` | Working directory (auto-created) |
| `model` | string | `qwen-cli/qwen-max` | Model identifier in responses |
| `cors.allowed_origins` | array | `["*"]` | CORS allowed origins |

## Driver Notes

### Qwen driver

Communicates via JSON-RPC over stdio (`--acp` flag).

**Install to user home (no admin required):**

```bash
# Set npm prefix to a user-local directory
npm config set prefix ~/.npm-global

# Add to PATH (add this line to ~/.bashrc or ~/.zshrc)
export PATH="$HOME/.npm-global/bin:$PATH"

# Install
npm install -g @qwen-code/qwen-code
```

On **Windows** (PowerShell, no admin):
```powershell
# npm installs to %APPDATA%\npm by default — already user-local
npm install -g @qwen-code/qwen-code
# Binary will be at %APPDATA%\npm\qwen.cmd
```

Set `cli.command` in config to the full path if `qwen` is not on PATH:
```json
"cli": { "command": "C:\\Users\\you\\AppData\\Roaming\\npm\\qwen.cmd" }
```

### OpenCode driver

- Starts `opencode serve` as a headless HTTP server on an auto-selected port
- Requires a configured AI provider: `opencode providers login`
- SSE events (`message.part.updated`, `session.status`) drive streaming

**Install to user home (no admin required):**

```bash
# Linux/macOS — installs to ~/.local/bin
curl -fsSL https://opencode.ai/install | sh
# or if the above sets a different path, check: which opencode

# Add to PATH if needed (add to ~/.bashrc or ~/.zshrc)
export PATH="$HOME/.local/bin:$PATH"
```

On **Windows** (PowerShell, no admin):
```powershell
irm https://opencode.ai/install.ps1 | iex
# Installs to %USERPROFILE%\.opencode\bin
# Add to PATH:
$env:PATH = "$env:USERPROFILE\.opencode\bin;$env:PATH"
```

After install, authenticate with your AI provider:
```bash
opencode providers login
```

## CLI Flags

| Flag | Description |
|------|-------------|
| `--config` | Path to configuration file |
| `--version` | Show version |

## License

MIT
