# ACPBridge Load Balancer Setup

## Overview

ACPBridge is load balanced across two VMs using Cloudflare DNS round-robin + Caddy reverse proxy.

## Architecture

```
Client → Cloudflare DNS (round-robin)
                ↓
        ┌───────┴───────┐
        ↓               ↓
   eu1.llm.bigt.ai  eu2.llm.bigt.ai
   79.76.113.140    85.214.37.95
        ↓               ↓
   Caddy:9090       Caddy:9090
   (eu.llm.bigt.ai  (eu.llm.bigt.ai
    → both VMs)      → both VMs)
```

## Servers

| Name | Hostname | IP | Provider | SSH User | SSH Key |
|------|----------|-----|----------|----------|---------|
| eu1 | `eu1.llm.bigt.ai` | `79.76.113.140` | Oracle | `ubuntu` | `~/.ssh/id_rsa` |
| eu2 | `eu2.llm.bigt.ai` | `85.214.37.95` | Strato | `root` | `~/.ssh/oraclevpc.key` |

## DNS Records (Cloudflare)

| Record | Type | Content | Proxied |
|--------|------|---------|---------|
| `eu.llm.bigt.ai` | A | `79.76.113.140` | No |
| `eu.llm.bigt.ai` | A | `85.214.37.95` | No |
| `eu1.llm.bigt.ai` | A | `79.76.113.140` | No |
| `eu2.llm.bigt.ai` | A | `85.214.37.95` | No |

Cloudflare free tier round-robins DNS requests to both IPs.

## Caddy Configuration

### eu1 (`/etc/caddy/Caddyfile.d/acpbridge.caddy`)
```
eu1.llm.bigt.ai {
    reverse_proxy localhost:9090
}
```

### eu1 (`/etc/caddy/Caddyfile.d/eu-llm-lb.caddy`)
```
eu.llm.bigt.ai {
    reverse_proxy 79.76.113.140:9090 85.214.37.95:9090
}
```

### eu2 (`/etc/caddy/Caddyfile.d/acpbridge.caddy`)
```
eu2.llm.bigt.ai {
    reverse_proxy localhost:9090
}
```

### eu2 (`/etc/caddy/Caddyfile.d/eu-llm-lb.caddy`)
```
eu.llm.bigt.ai {
    reverse_proxy localhost:9090 79.76.113.140:9090
}
```

## Deployment

### Deploy ACPBridge to Both VMs
```bash
# Deploy eu1 (Oracle)
cd /home/jcui/git/pm/acpbridge && bash deploy.sh all eu1

# Deploy eu2 (Strato)
cd /home/jcui/git/pm/acpbridge && bash deploy.sh all eu2
```

### Setup DNS Load Balancing
```bash
cd /home/jcui/git/pm/acpbridge && bash update-dns.sh lb eu.llm.bigt.ai 79.76.113.140 85.214.37.95 false
```

## Health Checks

```bash
# Direct ACPBridge health
ssh -i ~/.ssh/id_rsa ubuntu@79.76.113.140 'curl -s http://localhost:9090/health'
ssh -i ~/.ssh/oraclevpc.key root@85.214.37.95 'curl -s http://localhost:9090/health'

# Via Caddy (eu1)
ssh -i ~/.ssh/id_rsa ubuntu@79.76.113.140 'curl -s -H "Host: eu1.llm.bigt.ai" http://localhost:80/health'
ssh -i ~/.ssh/id_rsa ubuntu@79.76.113.140 'curl -s -H "Host: eu.llm.bigt.ai" http://localhost:80/health'

# Via Caddy (eu2)
ssh -i ~/.ssh/oraclevpc.key root@85.214.37.95 'curl -s -H "Host: eu2.llm.bigt.ai" http://localhost:80/health'
ssh -i ~/.ssh/oraclevpc.key root@85.214.37.95 'curl -s -H "Host: eu.llm.bigt.ai" http://localhost:80/health'
```

## Verification

```bash
# DNS resolution
dig @1.1.1.1 eu.llm.bigt.ai +short
dig @1.1.1.1 eu1.llm.bigt.ai +short
dig @1.1.1.1 eu2.llm.bigt.ai +short

# Container status
ssh -i ~/.ssh/id_rsa ubuntu@79.76.113.140 'sudo docker ps --format "{{.Names}}: {{.Status}}"'
ssh -i ~/.ssh/oraclevpc.key root@85.214.37.95 'docker ps --format "{{.Names}}: {{.Status}}"'

# Caddy config
ssh -i ~/.ssh/id_rsa ubuntu@79.76.113.140 'cat /etc/caddy/Caddyfile.d/*.caddy'
ssh -i ~/.ssh/oraclevpc.key root@85.214.37.95 'cat /etc/caddy/Caddyfile.d/*.caddy'
```

## Troubleshooting

### Caddy fails to start
```bash
# Check for duplicate site definitions
sudo journalctl -xeu caddy --no-pager -n 20

# Common causes:
# - Duplicate import in Caddyfile (e.g. `import Caddyfile.d/*` twice)
# - Stale autosave.json: `sudo rm /var/lib/caddy/.config/caddy/autosave.json`
# - Duplicate .caddy files in Caddyfile.d/
```

### Caddy reload fails with "connection refused"
This happens when `admin off` is set in the Caddyfile. Use `systemctl restart` instead of `reload`.

### DNS not resolving
```bash
# Check Cloudflare dashboard or API
source ~/.cloudflared/token.sh
curl -s -H "Authorization: Bearer $CLOUDFLARE_API_TOKEN" \
  "https://api.cloudflare.com/client/v4/zones/95d1db3d61bbb35a5abd6b4debde098e/dns_records?name=eu.llm.bigt.ai" | jq
```

## API Keys

| Instance | API Key |
|----------|---------|
| eu1 | `eu1-8a9f-1c3b-4d5e-9f0a-123456789abc` |
| eu2 | `eu2-8a9f-1c3b-4d5e-9f0a-123456789abc` |

## Notes

- Cloudflare proxy is **disabled** (proxied: false) — traffic goes directly to the VMs.
- Caddy runs on the host (not in Docker) and reverse proxies to `localhost:9090`.
- Each VM's Caddy also load-balances `eu.llm.bigt.ai` to **both** backends for redundancy.
- If one VM goes down, Cloudflare DNS continues routing to the other (the health check catches failures).