#!/bin/bash
set -e

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m' # No Color

echo -e "${GREEN}=== ACPBridge Deployment Script ===${NC}"

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Server selection: eu1 (default) or eu2
# Usage: ./deploy.sh <command> [eu1|eu2]
SERVER="${2:-eu1}"

case "$SERVER" in
  eu1)
    VM_IP="79.76.113.140"
    SSH_USER="ubuntu"
    SSH_KEY="$HOME/.ssh/id_rsa"
    API_KEY="eu1-8a9f-1c3b-4d5e-9f0a-123456789abc"
    ;;
  eu2)
    VM_IP="85.214.37.95"
    SSH_USER="root"
    SSH_KEY="$HOME/.ssh/oraclevpc.key"
    API_KEY="eu2-8a9f-1c3b-4d5e-9f0a-123456789abc"
    ;;
  *)
    echo -e "${RED}Unknown server: $SERVER. Use eu1 or eu2.${NC}"
    exit 1
    ;;
esac

echo -e "${GREEN}Target: $SERVER (${VM_IP}) as ${SSH_USER}${NC}"

# Derive home directory from user
if [ "$SSH_USER" = "root" ]; then
  SSH_HOME="/root"
else
  SSH_HOME="/home/${SSH_USER}"
fi

# Source directory (local acpbridge repo)
SOURCE_DIR="${SCRIPT_DIR}"
if [ ! -d "${SOURCE_DIR}" ]; then
  echo -e "${RED}ACPBridge source not found at ${SOURCE_DIR}${NC}"
  exit 1
fi

SSH_OPTS="-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o ConnectTimeout=10"
echo -e "${YELLOW}Using SSH key: ${SSH_KEY}${NC}"

if [ ! -f "${SSH_KEY}" ]; then
  echo -e "${RED}SSH key not found: ${SSH_KEY}${NC}"
  exit 1
fi

# Test SSH connection with retry
echo -e "${YELLOW}Testing SSH connection...${NC}"
MAX_RETRIES=5
for i in $(seq 1 $MAX_RETRIES); do
    if ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} "echo 'SSH OK'" 2>/dev/null; then
        echo -e "${GREEN}Connected via SSH as ${SSH_USER}${NC}"
        break
    else
        echo -e "${YELLOW}Attempt $i/$MAX_RETRIES failed. Waiting 5s...${NC}"
        sleep 5
    fi
    if [ $i -eq $MAX_RETRIES ]; then
        echo -e "${RED}Failed to connect via SSH. Server might be overloaded or down.${NC}"
        exit 1
    fi
done

# Setup VM (install Docker if missing)
setup_vm() {
  echo -e "${YELLOW}Setting up VM environment...${NC}"

  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} << 'ENDSSH'
    set -e

    echo "Updating system..."
    sudo apt-get update -y

    echo "Installing Docker..."
    if ! command -v docker &> /dev/null; then
      curl -fsSL https://get.docker.com -o get-docker.sh
      sudo sh get-docker.sh
      sudo usermod -aG docker $USER
      rm get-docker.sh
    fi

    echo "Docker version:"
    docker --version
    echo "Setup complete!"
ENDSSH
}

# Sync Qwen OAuth token from local machine to server
sync_qwen_token() {
  echo -e "${YELLOW}Syncing Qwen OAuth token...${NC}"

  LOCAL_TOKEN="$HOME/.qwen/oauth_creds.json"
  if [ ! -f "$LOCAL_TOKEN" ]; then
    echo -e "${RED}Local OAuth token not found at $LOCAL_TOKEN${NC}"
    echo -e "${YELLOW}Run: npx @qwen-code/qwen-code@latest auth qwen-oauth${NC}"
    return 1
  fi

  # Check expiry
  EXPIRY_MS=$(python3 -c "import json; print(json.load(open('$LOCAL_TOKEN'))['expiry_date'])" 2>/dev/null)
  if [ -n "$EXPIRY_MS" ]; then
    EXPIRY_S=$((EXPIRY_MS / 1000))
    NOW=$(date +%s)
    DIFF=$((EXPIRY_S - NOW))
    DIFF_MIN=$((DIFF / 60))
    EXPIRY_DATE=$(date -d @$EXPIRY_S 2>/dev/null || date -r $EXPIRY_S 2>/dev/null)

    if [ "$DIFF" -le 0 ]; then
      echo -e "${RED}Token EXPIRED at $EXPIRY_DATE${NC}"
      echo -e "${YELLOW}Run: npx @qwen-code/qwen-code@latest auth qwen-oauth${NC}"
      return 1
    elif [ "$DIFF" -lt 7200 ]; then
      echo -e "${YELLOW}Token expires in ${DIFF_MIN}min ($EXPIRY_DATE)${NC}"
    else
      echo -e "${GREEN}Token valid for ${DIFF_MIN}min (expires: $EXPIRY_DATE)${NC}"
    fi
  fi

  # Create remote .qwen dir and copy token
  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} << 'ENDSSH'
    set -e
    sudo rm -rf ~/.qwen
    mkdir -p ~/.qwen ~/qwen-ws
    sudo chmod 777 ~/.qwen ~/qwen-ws
ENDSSH

  # Copy token
  scp ${SSH_OPTS} -i ${SSH_KEY} ${LOCAL_TOKEN} ${SSH_USER}@${VM_IP}:~/.qwen/oauth_creds.json

  # Fix permissions on server
  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} << 'ENDSSH'
    set -e
    sudo chmod -R 777 ~/.qwen ~/qwen-ws
    echo "Token synced and permissions set."
ENDSSH

  echo -e "${GREEN}OAuth token synced to ${VM_IP}${NC}"
}

# Deploy ACPBridge
deploy_acpbridge() {
  echo -e "${YELLOW}Deploying ACPBridge server...${NC}"

  REMOTE_DIR="~/acpbridge-deploy"

  # Create clean directory on VM
  echo "Creating deployment directory..."
  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} "mkdir -p ${REMOTE_DIR}"

  # Sync source files to VM (exclude scripts we don't need on server)
  echo "Syncing source files to VM..."
  rsync -avz --no-owner --no-group \
    --exclude='.git/' \
    --exclude='deploy.sh' \
    --exclude='qwen-token-check.sh' \
    --exclude='qwenrenew.sh' \
    --exclude='update-dns.sh' \
    -e "ssh ${SSH_OPTS} -i ${SSH_KEY}" \
    "${SOURCE_DIR}/" \
    ${SSH_USER}@${VM_IP}:${REMOTE_DIR}/

  # Pull and start services
  echo "Pulling latest ACPBridge image from GHCR..."

  # Get GHCR auth token
  GHCR_TOKEN=$(gh auth token)

  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} \
    "export SSH_HOME='${SSH_HOME}'; export API_KEY='${API_KEY}'; bash -s" << 'ENDSSH'
    set -e
    cd ~/acpbridge-deploy

    # Pass GHCR token via temp file
    GHCR_TOKEN=$(cat /tmp/ghcr_token 2>/dev/null || echo "")
    if [ -n "$GHCR_TOKEN" ]; then
      echo "$GHCR_TOKEN" | sudo docker login ghcr.io -u j0904 --password-stdin
    fi

    # Stop previous deployment
    sudo docker compose down --remove-orphans 2>/dev/null || true
    sudo docker rm -f acpbridge caddy 2>/dev/null || true

    # Create workspace directory
    mkdir -p ~/qwen-ws

    # Ensure .qwen permissions are correct for container
    chmod -R 777 ~/.qwen 2>/dev/null || true

    # Write opencode config with correct model
    mkdir -p ~/.picoclaw
    cat > ~/.picoclaw/config.json << 'EOFCONFIG'
{
  "session": {"dm_scope": "per-channel-peer"},
  "version": 2,
  "agents": {
    "defaults": {
      "workspace": "~/.picoclaw/workspace",
      "restrict_to_workspace": false,
      "allow_read_outside_workspace": false,
      "provider": "",
      "model_name": "opencode/deepseek-v4-flash-free",
      "max_tokens": 8192,
      "max_tool_iterations": 50
    }
  },
  "model_list": [
    {"model_name": "opencode/deepseek-v4-flash-free", "model": "opencode/deepseek-v4-flash-free"}
  ]
}
EOFCONFIG
    chmod 777 ~/.picoclaw/config.json

    # Generate correct docker-compose.yml
    sudo tee docker-compose.yml > /dev/null <<DCYML
services:
  acpbridge:
    image: ghcr.io/j0904/acpbridge:bufio-fix
    container_name: acpbridge
    command: --config /opt/acpbridge/config.json
    ports:
      - "9090:9090"
    volumes:
      - ./config.opencode.json:/opt/acpbridge/config.json
      - ${SSH_HOME}/opencode-ws:/home/app/.picoclaw/opencode-ws
      - ${SSH_HOME}/.picoclaw/config.json:/home/app/.picoclaw/config.json
      - ${SSH_HOME}/.qwen:/home/app/.qwen
      - /usr/lib/node_modules/opencode-ai/bin/.opencode:/usr/bin/.opencode:ro
    networks:
      - acpbridge-net

networks:
  acpbridge-net:
    driver: bridge

volumes:
  caddy_data:
  caddy_config:
DCYML

    echo "docker-compose.yml written."

    # Always write correct config.json with --auth-type=qwen-oauth
    echo "Writing config.opencode.json..."
    sudo tee config.opencode.json > /dev/null <<CFGJSON
{
  "listen": ":9090",
  "api_key": "${API_KEY}",
  "driver": "opencode",
  "cli": {
    "command": ".opencode",
    "args": [],
    "workspace": "/home/app/.picoclaw/opencode-ws"
  },
  "model": "opencode/deepseek-v4-flash-free"
}
CFGJSON

    # Pull latest image from GHCR
    sudo docker compose pull

    # Start services
    echo "Starting services..."
    sudo docker compose up -d

    echo "Waiting for services to start..."
    sleep 15

    echo ""
    echo "Container status:"
    sudo docker compose ps

    echo ""
    echo "Logs:"
    sudo docker logs --tail=15 acpbridge 2>&1

    echo ""
    echo "Health check:"
    HEALTH="failed"
    for i in $(seq 1 10); do
      HEALTH=$(timeout 5 curl -s http://localhost:9090/health 2>/dev/null || echo "failed")
      if echo "$HEALTH" | grep -q '"status":"ok"'; then break; fi
      sleep 3
    done
    echo "$HEALTH"

    if echo "$HEALTH" | grep -q '"status":"ok"'; then
      echo ""
      echo -e "${GREEN}✅ ACPBridge is healthy!${NC}"
    else
      echo ""
      echo -e "${YELLOW}⚠️  Health check did not return ok. Check: sudo docker logs acpbridge${NC}"
    fi
ENDSSH
}

# Pass GHCR token to server for docker login
pass_ghcr_token() {
  GHCR_TOKEN=$(gh auth token 2>/dev/null || echo "")
  if [ -n "$GHCR_TOKEN" ]; then
    echo "$GHCR_TOKEN" | ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} "cat > /tmp/ghcr_token && chmod 600 /tmp/ghcr_token"
    echo -e "${GREEN}GHCR token passed to server${NC}"
  fi
}

# Show status
show_status() {
  echo -e "${GREEN}=== Deployment Status ===${NC}"
  ssh ${SSH_OPTS} -i ${SSH_KEY} ${SSH_USER}@${VM_IP} << 'ENDSSH'
    echo "Docker containers:"
    sudo docker compose -f ~/acpbridge-deploy/docker-compose.yml ps
    echo ""
    echo "Open ports:"
    sudo ss -tlnp | grep -E ':(80|443|9090)'
    echo ""
    echo "Health check:"
    timeout 10 curl -s http://localhost:9090/health 2>/dev/null || echo "unreachable"
    echo ""
    echo "Recent logs:"
    sudo docker logs --tail=20 acpbridge 2>&1
ENDSSH
}

# Main execution
main() {
  case "${1:-all}" in
    setup) setup_vm ;;
    token) sync_qwen_token ;;
    deploy) pass_ghcr_token; deploy_acpbridge ;;
    status) show_status ;;
    all) setup_vm; pass_ghcr_token; deploy_acpbridge; show_status ;;
    *) echo "Usage: $0 {setup|token|deploy|status|all} [eu1|eu2]"; exit 1 ;;
  esac
}

main "$@"
