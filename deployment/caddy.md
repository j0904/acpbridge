# Caddy Multi-User Architecture

## Overview

Caddy runs on the **host** (not in Docker) and reverse-proxies HTTPS traffic to per-user oauth2-proxy containers. Each user has an isolated Docker network. A shared `caddy_net` Docker network allows Caddy to reach oauth2-proxy containers without host port binding.

## Architecture

```
Client → HTTPS user.s1001.bigt.ai
    ↓
Caddy (host, port 443)
    ↓ TCP/IP (via caddy_net bridge IP)
oauth2-<user>:10001 (no host ports)
    ↓ HTTP (internal Docker DNS)
code-server:8443
```

- Caddy resolves each user domain → Let's Encrypt TLS (auto-provisioned)
- Caddy proxies to oauth2-proxy's IP on `caddy_net` (e.g. `172.20.6.2:10001`)
- oauth2-proxy runs on port `10001` (fixed, no host port needed)
- oauth2-proxy forwards authenticated requests to code-server on port `8443`
- code-server has **no host ports** — only reachable via Docker DNS on `cs-net-<user>`

## File Layout

```
/etc/caddy/
├── Caddyfile            # Global config + import of Caddyfile.d/*
└── Caddyfile.d/
    ├── acpbridge.caddy
    ├── bigt-ai.caddy
    ├── eu-llm-lb.caddy
    ├── m16-bigtangle-org.caddy
    ├── testcui-inasset-de.caddy   # One per user
    └── ...                          # More user files as created
```

## Main Caddyfile (`/etc/caddy/Caddyfile`)

```
{}

import Caddyfile.d/*
```

- Global options block is empty (`{}`)
- `admin off` must **not** be present — it disables `systemctl reload caddy`
- Imports all `.caddy` files in `Caddyfile.d/`

## Per-User Caddy Block

```
# User subdomain - Caddy auto-provisions Let's Encrypt TLS
testcui-inasset-de.s1001.bigt.ai {
    reverse_proxy 172.20.6.2:10001
}
```

- Domain name (no `http://` prefix) → Caddy auto-TLS
- Upstream IP is the container's IP on `caddy_net` (discovered at deploy time)

## Docker Network: `caddy_net`

**Created once per server:**

```bash
docker network create --driver bridge --attachable caddy_net
```

- Shared bridge network that all user oauth2-proxy containers attach to
- Host Caddy has direct layer-3/IP access to this network
- Containers get IPs like `172.20.x.2` automatically
- No Docker DNS for host processes, so Caddy uses the resolved IP directly

## Deploy Flow

1. Ensure `caddy_net` exists
2. Deploy user's docker-compose stack (creates `cs-net-<user>` + attaches oauth2-proxy to `caddy_net`)
3. Discover oauth2-proxy's IP on `caddy_net`:
   ```bash
   docker inspect oauth2-<user> \
     --format '{{(index .NetworkSettings.Networks "caddy_net").IPAddress}}'
   ```
4. Write Caddy config file in `/etc/caddy/Caddyfile.d/<user>.caddy`
5. Apply: `systemctl reload caddy`

## User Docker Compose Template

```yaml
services:
  oauth2-proxy:
    # ...
    ports: []                          # No host ports!
    networks:
      - cs-net-${USER}                 # Internal: code-server access
      - caddy_net                       # Shared: Caddy access

networks:
  cs-net-${USER}:
    driver: bridge
  caddy_net:
    external: true
```

## Caddy Reload

```bash
systemctl reload caddy     # Zero-downtime reload (graceful)
systemctl restart caddy    # Fallback if admin API not available
```

- `caddy reload` requires the admin API. If `admin off` was set, use `systemctl restart` instead (brief downtime).
- `systemctl reload caddy` sends SIGHUP — works with or without admin API (as long as `admin off` is not set).

## Troubleshooting

### SSL Protocol Error (`tlsv1 alert internal error`)

**Cause:** Caddy has no site definition for the requested domain. TLS handshake starts but no certificate can be served.

**Check:**
```bash
ls /etc/caddy/Caddyfile.d/                          # Config exists?
sudo grep -r "$DOMAIN" /etc/caddy/Caddyfile.d/      # Site defined?
```

**Fix:** Create `/etc/caddy/Caddyfile.d/<user>.caddy` with the correct domain and proxy target, then reload Caddy.

### Caddy reload fails with "connection refused"

**Cause:** `admin off` is set in the global Caddyfile, disabling the admin API endpoint.

**Fix:**
```bash
sudo sed -i "/admin off/d" /etc/caddy/Caddyfile
systemctl restart caddy                              # One-time restart needed
systemctl reload caddy                               # Works afterwards
```

### Container IP not reachable from Caddy

**Check:**
```bash
docker inspect oauth2-<user> --format '{{(index .NetworkSettings.Networks "caddy_net").IPAddress}}'
docker exec oauth2-<user> curl -s http://localhost:10001/oauth2/health
```

**Fix:** Ensure container is connected to `caddy_net`:
```bash
docker network connect caddy_net oauth2-<user>
```
Then update Caddy config with the new IP and reload.

## Port Management

- All oauth2-proxy containers listen on port **10001** internally
- No host port consumed — containers are reachable only via `caddy_net` IPs
- This eliminates port scanning/reservation for multi-user deployments
- Theoretical max: 253 containers per `caddy_net` (/24 subnet) — scale by adding more shared networks if needed