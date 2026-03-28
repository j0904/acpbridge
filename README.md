# bridge-acp

A standalone HTTP bridge server that wraps Qwen ACP (Agent Communication Protocol) CLI, exposing it as an HTTP API.

## Architecture

```
HTTP Client → bridge-acp (:9090) → qwen --acp (stdio) → Remote LLM
```

## Features

- **HTTP API**: REST endpoints for sync/streaming chat
- **OpenAI Compatible**: `/v1/chat/completions` endpoint
- **SSE Streaming**: Real-time token streaming
- **ACP Protocol**: Full support for Qwen ACP JSON-RPC protocol
- **Approval Mode**: Configurable (default: `--approval-mode=yolo`)
- **Cross-platform**: Windows, macOS, Linux

## Quick Start

### 1. Install Go (if not installed)

Download from: https://go.dev/dl/

### 2. Build

```bash
cd bridge-acp
go mod tidy
go build -o bridge-acp.exe ./cmd/bridge-acp
```

### 3. Configure

Copy `config.example.json` to `config.json`:

```bash
cp config.example.json config.json
```

Edit `config.json`:

```json
{
  "listen": ":9090",
  "api_key": "",
  "cli": {
    "command": "npx",
    "args": ["@qwen-code/qwen-code@latest", "--approval-mode=yolo"],
    "workspace": "~/.picoclaw/qwen-ws"
  },
  "model": "qwen-cli/qwen-max"
}
```

### 4. Run

```bash
./bridge-acp.exe --config config.json
```

Or with environment:

```bash
PICOCLAW_CONFIG=config.json ./bridge-acp.exe
```

## API Endpoints

### Health Check

```bash
GET /health
```

Response:
```json
{"status": "ok"}
```

### Sync Chat

```bash
POST /chat
Content-Type: application/json

{"message": "Hello, how are you?"}
```

Response:
```json
{
  "reply": "I'm doing well, thank you!",
  "tokens": 5,
  "model": "qwen-cli/qwen-max"
}
```

### Streaming Chat (SSE)

```bash
POST /chat/stream
Content-Type: application/json

{"message": "Tell me a story"}
```

Response (SSE events):
```
data: {"chunk":"Once"}
data: {"chunk":" upon"}
data: {"chunk":" a"}
data: {"chunk":" time"}
data: {"done":true}
```

### OpenAI Compatible

```bash
POST /v1/chat/completions
Content-Type: application/json
Authorization: Bearer your-api-key

{
  "model": "qwen-cli/qwen-max",
  "messages": [
    {"role": "user", "content": "Hello"}
  ],
  "stream": false
}
```

## Configuration

| Field | Type | Default | Description |
|-------|------|---------|-------------|
| `listen` | string | `:9090` | HTTP listen address |
| `api_key` | string | `""` | API key for authentication (empty = no auth) |
| `cli.command` | string | `qwen` | CLI command to run |
| `cli.args` | array | `["--approval-mode=yolo"]` | CLI arguments |
| `cli.workspace` | string | `~/.picoclaw/qwen-ws` | Working directory |
| `model` | string | `qwen-cli/qwen-max` | Model identifier |
| `cors.allowed_origins` | array | `["*"]` | CORS allowed origins |

## Usage with PicoClaw

Add to `config.acp.json`:

```json
{
  "model_name": "qwen-bridge",
  "model": "openai/qwen-code",
  "api_base": "http://localhost:9090/v1",
  "api_key": "none"
}
```

Then start bridge-acp first:

```bash
./bridge-acp.exe --config config.json
```

## CLI Options

| Flag | Description |
|------|-------------|
| `--config` | Path to configuration file |
| `--version` | Show version |

## Examples

### Using curl

```bash
# Sync chat
curl -X POST http://localhost:9090/chat \
  -H "Content-Type: application/json" \
  -d '{"message": "What is 2+2?"}'

# Streaming
curl -X POST http://localhost:9090/chat/stream \
  -H "Content-Type: application/json" \
  -d '{"message": "Explain quantum computing"}'

# OpenAI compatible
curl -X POST http://localhost:9090/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer none" \
  -d '{
    "model": "qwen-cli/qwen-max",
    "messages": [{"role": "user", "content": "Hello"}]
  }'
```

### Using with approval mode

```json
{
  "cli": {
    "command": "npx",
    "args": ["@qwen-code/qwen-code@latest", "--approval-mode=yolo"]
  }
}
```

### Using global install

```json
{
  "cli": {
    "command": "C:\\Users\\you\\AppData\\Roaming\\npm\\qwen.cmd",
    "args": ["--approval-mode=yolo"]
  }
}
```

## Troubleshooting

### "qwen command not found"

Install Qwen CLI:
```bash
npm install -g @qwen-code/qwen-code
```

Or use npx in config:
```json
{
  "cli": {
    "command": "npx",
    "args": ["@qwen-code/qwen-code@latest", "--approval-mode=yolo"]
  }
}
```

### Connection refused

Make sure bridge-acp is running:
```bash
curl http://localhost:9090/health
```

### Authentication failed

Set `api_key` to empty string for no auth, or provide correct key:
```json
{
  "api_key": ""
}
```

## License

MIT
