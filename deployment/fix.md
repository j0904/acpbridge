# ACPBridge Deployment & Fix Notes

---

## eu.llm.bigt.ai Load Balancer (2026-04-18)

### Goal
`https://eu.llm.bigt.ai` round-robin across eu1 (79.76.113.140) and eu2 (85.214.37.95).

### Problem 1: DNS round-robin not viable
Using two A records in Cloudflare DNS would route ~50% of clients to eu1 directly on port 443,
but eu1's Caddy couldn't get a cert for `eu.llm.bigt.ai` (ZeroSSL EAB credentials failing,
error `caddy_legacy_user_removed`). Half of HTTPS clients would get TLS errors.

**Fix:** Single DNS A record → eu2. eu2's Caddy holds the cert and load balances upstream.

### Problem 2: Caddy can't reach eu1:9090 from eu2
eu1 (Hetzner) blocks external access to port 9090 at the cloud firewall level.
Port 443 is open. iptables allows 1001-65535 but Hetzner external firewall policy overrides it.

**Fix:** SSH tunnel from eu2 to eu1.

```bash
# On eu2: generate tunnel key
ssh-keygen -t ed25519 -f ~/.ssh/eu1_tunnel_key -N '' -C 'eu2-to-eu1-tunnel'

# On eu1: add to authorized_keys (restricted to port-forward only)
echo 'no-pty,no-agent-forwarding,no-X11-forwarding,permitopen="localhost:9090" <pubkey>' \
  >> ~/.ssh/authorized_keys
```

Persistent tunnel as systemd service on eu2 (`/etc/systemd/system/eu1-tunnel.service`):
forwards `eu2:19090 → eu1:9090`.

### Problem 3: Caddy `https_port 80` blocks ACME for all vhosts
eu2's Caddyfile had `https_port 80` (set for Cloudflare-proxied code-server users).
This prevents Caddy from listening on 443 or doing ACME for ANY named vhost.

**Fix:** Remove `https_port 80` from eu2's global Caddyfile. Cloudflare-proxied subdomains
arrive on port 80 regardless — `http_port 80` alone is sufficient for them.

### Problem 4: Caddy mixed-scheme upstreams not supported
`reverse_proxy http://localhost:9090 https://eu1.llm.bigt.ai` fails:
> "for now, all proxy upstreams must use the same scheme"

**Fix:** Route eu1 traffic via the tunnel (`localhost:19090`) so both upstreams are plain HTTP.

### Final Configuration

**DNS:** `eu.llm.bigt.ai → 85.214.37.95` (proxied: false)

**eu2 Caddyfile** (`/etc/caddy/Caddyfile`):
```
{
    http_port 80
}
import /etc/caddy/Caddyfile.d/*.caddy
:80 { respond "bigt.ai Code Server" 404 }
```

**`/etc/caddy/Caddyfile.d/eu-llm.caddy`** (on eu2):
```
eu.llm.bigt.ai {
    reverse_proxy localhost:9090 localhost:19090 {
        lb_policy round_robin
        health_uri /health
        health_interval 30s
        health_timeout 5s
        health_status 200
    }
}
```

**`/etc/systemd/system/eu1-tunnel.service`** (on eu2):
```ini
[Unit]
Description=SSH tunnel to eu1 acpbridge
After=network.target

[Service]
Type=simple
ExecStart=/usr/bin/autossh -N \
  -o StrictHostKeyChecking=no \
  -o ServerAliveInterval=30 \
  -o ServerAliveCountMax=3 \
  -i /root/.ssh/eu1_tunnel_key \
  -L 19090:localhost:9090 \
  ubuntu@79.76.113.140
Restart=always
RestartSec=10

[Install]
WantedBy=multi-user.target
```

### API Keys
| Endpoint | Key |
|---|---|
| `eu.llm.bigt.ai` | `eu2-8a9f-1c3b-4d5e-9f0a-123456789abc` |
| `eu1.llm.bigt.ai` | `eu2-8a9f-1c3b-4d5e-9f0a-123456789abc` (updated to match) |
| `eu2.llm.bigt.ai` | `eu2-8a9f-1c3b-4d5e-9f0a-123456789abc` |

### Verify
```bash
# Health + cert
curl -sv https://eu.llm.bigt.ai/health 2>&1 | grep -E "subject|verify|HTTP"

# Round-robin (alternating ~5s eu1 / ~2s eu2 latency)
for i in 1 2 3 4; do
  curl -s -o /dev/null -w "req $i: %{time_total}s\n" \
    -X POST https://eu.llm.bigt.ai/v1/chat/completions \
    -H 'Authorization: Bearer eu2-8a9f-1c3b-4d5e-9f0a-123456789abc' \
    -H 'Content-Type: application/json' \
    -d '{"model":"opencode/minimax-m2.5-free","messages":[{"role":"user","content":"hi"}],"max_tokens":5}'
done
```

---

## eu2 / s1001 Server Setup (2026-04-18)

### Server: 85.214.37.95 (Strato, root, ~/.ssh/oraclevpc.key)

- Docker installed via `setup-new-server.sh`
- Caddy installed, global config: `http_port 80` only (no `https_port 80`)
- acpbridge deployed, binary mounted from host: `/usr/lib/node_modules/opencode-ai/bin/.opencode`
- Docker daemon pool: `172.20.0.0/14` (changed from default `172.17.0.0/16` which conflicted)
- opencode binary copied from eu1 (npm postinstall doesn't work on root)

### deploy.sh usage
```bash
./deploy.sh deploy       # → eu1 (default)
./deploy.sh deploy eu2   # → eu2
./deploy.sh status eu2
```

---

# ACPBridge Deployment & Qwen Fix Notes (original)

## Problem
The acpbridge container on vm2 was crashing with:
```
Error creating server: failed to create session: RPC error -32000: Authentication required
```
Qwen Code CLI required OAuth authentication that wasn't configured in the Docker container.

## Root Cause
1. The Dockerfile copied `config.opencode.json` as `config.json` (wrong config, opencode instead of qwen)
2. The container runs as `app` user with `HOME=/home/app`, but qwen OAuth credentials weren't available at `/home/app/.qwen/`
3. The `@qwen-code/qwen-code` npm package requires interactive OAuth browser auth which doesn't work in headless containers

## Solution

### 1. Fixed Dockerfile.acpbridge
- Changed `COPY config.opencode.json config.json` → `COPY config.json config.json`
- Changed `npm install -g opencode-ai` → `npm install -g @qwen-code/qwen-code@latest`

### 2. Fixed config.json
```json
{
  "listen": ":9091",
  "cli": {
    "command": "qwen",
    "args": ["--approval-mode=yolo"],
    "workspace": "~/.picoclaw/qwen-ws"
  },
  "model": "qwen-cli/qwen-max"
}
```

### 3. Qwen OAuth Authentication
Run on the VM to authenticate:
```bash
# Install qwen CLI on VM
sudo npm install -g @qwen-code/qwen-code@latest

# Start auth (will print URL with user_code)
qwen auth qwen-oauth
```
Open the URL in a browser and authorize with Google account.

### 4. docker-compose.yml Volume Mounts
```yaml
services:
  acpbridge:
    image: ghcr.io/j0904/acpbridge:latest
    container_name: acpbridge
    ports:
      - "9091:9091"
    volumes:
      - /home/ubuntu/.qwen:/home/app/.qwen
      - /home/ubuntu/qwen-ws:/home/app/.picoclaw/qwen-ws
    networks:
      - acpbridge-net
```

Key: Mount the qwen OAuth credentials directory (`~/.qwen`) into the container at `/home/app/.qwen` so the app user can find the auth tokens.

### 5. GHCR Image Publishing
- Added `Dockerfile` and `.github/workflows/docker-publish.yml` to acpbridge repo
- Triggered by tag push (`v*`)
- Pushes to `ghcr.io/j0904/acpbridge:latest`
- Repository workflow permissions must be set to **write** (Settings → Actions → General)
- GitHub auth needs `write:packages` scope: `gh auth refresh -s write:packages`

## Deploy Commands
```bash
# Deploy to vm2 (pulls from GHCR)
bash deploy.sh deploy

# Check status
bash deploy.sh status

# Check health
curl http://138.2.152.171:9091/health
# Expected: {"driver":"qwen","session":"active","status":"ok"}
```

## OCI CLI Setup (for rebooting VM)
```bash
# Generate API key pair
mkdir -p ~/.oci/sessions/DEFAULT
openssl genrsa -out ~/.ssh/oraclevpc.key 2048
openssl rsa -in ~/.ssh/oraclevpc.key -pubout -outform DER | openssl md5 -c

# Create config
cat > ~/.oci/config << EOF
[DEFAULT]
user=ocid1.user.oc1..aaaaaaaafhjg2jvr3pnwkl57whp6fszfdx7u5afeylwdm7yo72fcc52y4vrq
fingerprint=<YOUR_FINGERPRINT>
tenancy=ocid1.tenancy.oc1..aaaaaaaagvof5hewab5mwktld64hfnmrwv7mz5e7jez4v5ylq6swl2kveawa
region=eu-frankfurt-1
key_file=/home/cui/.ssh/oraclevpc.key
EOF

# Upload public key to OCI Console: Identity → Users → your user → API Keys → Add Public Key
# Then test:
oci compute instance action --instance-id <INSTANCE_OCID> --action RESET --wait-for-state RUNNING
```
