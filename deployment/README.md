# ACPBridge

Deploy and manage ACPBridge — a proxy that exposes Qwen Code CLI as an OpenAI-compatible API endpoint.

## Architecture

```
Client ──HTTPS──▶ Caddy (443) ──▶ acpbridge (:9091) ──▶ qwen CLI (--acp)
```

- **acpbridge**: Bridges the Qwen Code CLI to an OpenAI-compatible `/v1/chat/completions` API
- **Caddy**: Reverse proxy with automatic TLS (Let's Encrypt)
- **qwen CLI**: Runs via OAuth authentication, mounted into the container via `~/.qwen`

## Endpoints

| Endpoint | URL | Description |
|----------|-----|-------------|
| Health | `https://eu1.llm.bigt.ai/health` | Service status check |
| Chat | `https://eu1.llm.bigt.ai/v1/chat/completions` | OpenAI-compatible chat completions |

### Example Request

```bash
curl -sk https://eu1.llm.bigt.ai/v1/chat/completions \
  -X POST -H "Content-Type: application/json" \
  -d '{"model":"gpt-4","messages":[{"role":"user","content":"Hello"}]}'
```

## Deployment

### Prerequisites
- SSH key at `~/.ssh/id_rsa`
- GitHub CLI authenticated (`gh auth login`) with `write:packages` scope
- Oracle VM running Ubuntu (configured in `deploy.sh`)

### Deploy

```bash
./deploy.sh deploy       # Pull from GHCR, sync files, start containers
./deploy.sh status       # Check container status and logs
./deploy.sh setup        # Install Docker on VM
./deploy.sh all          # Setup + deploy + status
```

### DNS

```bash
./update-dns.sh          # Update Cloudflare DNS (auto-sources credentials from ~/.cloudflared/token.sh)
```

## Qwen OAuth Token Management

The Qwen OAuth token expires periodically. Use these tools to manage it.

### Manual Renewal

```bash
./qwenrenew.sh
```

This will:
1. Run `npx @qwen-code/qwen-code@latest auth qwen-oauth` (opens browser URL)
2. Copy the fresh token to the VM at `~/.qwen/oauth_creds.json`
3. Restart the acpbridge container
4. Verify health endpoint

### Automatic Expiry Check

A cron job runs every 6 hours to check token expiry:

```
0 */6 * * *  /home/cui/git/pm/acpbridge/qwen-check-expiry.sh
```

- If token expires within **2 hours**, logs a warning to `/tmp/qwen-token-check.log`
- If token is **already expired**, auto-copies the local token to the VM and restarts the container

### Manual Token Copy (Quick)

```bash
# After local auth: qwen auth qwen-oauth (or npx ...)
scp ~/.qwen/oauth_creds.json ubuntu@138.2.152.171:~/.qwen/
ssh ubuntu@138.2.152.171 "cd ~/acpbridge-deploy && sudo docker compose restart acpbridge"
```

## Files

| File | Description |
|------|-------------|
| `deploy.sh` | Main deployment script (SSH + rsync + docker-compose) |
| `update-dns.sh` | Update Cloudflare DNS A record (auto-sources `~/.cloudflared/token.sh`) |
| `docker-compose.yml` | acpbridge + Caddy services |
| `docker-compose-all.yml` | All services including opencode (if needed) |
| `Caddyfile` | Reverse proxy config for eu1.llm.bigt.ai |
| `config.json` | ACPBridge configuration for qwen driver |
| `config.opencode.json` | ACPBridge configuration for opencode driver |
| `qwenrenew.sh` | Interactive OAuth renewal script |
| `qwen-check-expiry.sh` | Cron script for automatic expiry detection |

## Troubleshooting

### Container crashes on startup
Check logs:
```bash
ssh ubuntu@138.2.152.171 "cd ~/acpbridge-deploy && sudo docker compose logs acpbridge"
```

Common cause: expired OAuth token. Run `./qwenrenew.sh`.

### High system load
Stop any crashing containers:
```bash
ssh ubuntu@138.2.152.171 "cd ~/acpbridge-deploy && sudo docker compose stop acpbridge-opencode"
```

### Health check fails
```bash
curl -sk https://eu1.llm.bigt.ai/health
# Expected: {"driver":"qwen","session":"active","status":"ok"}
```

### VM unreachable
Reboot via Oracle Cloud Console or OCI CLI:
```bash
oci compute instance action --instance-id <OCID> --action RESET
```
