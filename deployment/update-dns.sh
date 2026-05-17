#!/bin/bash
set -e

# Source Cloudflare credentials
if [ -f ~/.cloudflared/token.sh ]; then
  source ~/.cloudflared/token.sh
fi

# Normalize: support both CF_API_TOKEN and CLOUDFLARE_API_TOKEN
if [ -z "$CF_API_TOKEN" ] && [ -n "$CLOUDFLARE_API_TOKEN" ]; then
  CF_API_TOKEN="$CLOUDFLARE_API_TOKEN"
fi

# Colors for output
RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
BLUE='\033[0;34m'
NC='\033[0m'

echo -e "${GREEN}=== Cloudflare DNS Update Script ===${NC}"

# Configuration
DOMAIN="bigt.ai"
MODE="${1:-}"
RECORD_NAME="${2:-}"

show_usage() {
  echo "Usage: $0 <mode> [options]"
  echo ""
  echo "Modes:"
  echo "  a <record-name> <ip>       - Create/update single A record"
  echo "  cname <record-name> <target> - Create CNAME record"
  echo "  lb <record-name> <ip1> <ip2> - Create load balancing A records (round-robin)"
  echo "  weighted <record-name> <target1> <weight1> <target2> <weight2> - Weighted CNAME (pro)"
  echo ""
  echo "Examples:"
  echo "  $0 a eu1.llm.bigt.ai 85.214.37.95"
  echo "  $0 cname eu.llm.bigt.ai eu1.llm.bigt.ai"
  echo "  $0 lb eu.llm.bigt.ai 85.214.37.95 85.214.37.96"
  exit 1
}

if [ -z "$MODE" ]; then
  show_usage
fi

# Set auth headers based on what's available
if [ -n "$CF_API_TOKEN" ]; then
  AUTH_METHOD="token"
  echo -e "${GREEN}Using API Token for authentication${NC}"
elif [ -n "$CF_API_KEY" ] && [ -n "$CF_EMAIL" ]; then
  AUTH_METHOD="key"
  echo -e "${GREEN}Using Global API Key for authentication${NC}"
else
  echo -e "${RED}Error: CF_EMAIL is required when using CF_API_KEY${NC}"
  exit 1
fi

# Function to make API calls
cf_api() {
  local method=$1
  local endpoint=$2
  local data=$3

  if [ "$AUTH_METHOD" = "token" ]; then
    if [ -n "$data" ]; then
      curl -s -X "$method" "https://api.cloudflare.com/client/v4$endpoint" \
        -H "Authorization: Bearer $CF_API_TOKEN" \
        -H "Content-Type: application/json" \
        -d "$data"
    else
      curl -s -X "$method" "https://api.cloudflare.com/client/v4$endpoint" \
        -H "Authorization: Bearer $CF_API_TOKEN" \
        -H "Content-Type: application/json"
    fi
  else
    if [ -n "$data" ]; then
      curl -s -X "$method" "https://api.cloudflare.com/client/v4$endpoint" \
        -H "X-Auth-Email: $CF_EMAIL" \
        -H "X-Auth-Key: $CF_API_KEY" \
        -H "Content-Type: application/json" \
        -d "$data"
    else
      curl -s -X "$method" "https://api.cloudflare.com/client/v4$endpoint" \
        -H "X-Auth-Email: $CF_EMAIL" \
        -H "X-Auth-Key: $CF_API_KEY" \
        -H "Content-Type: application/json"
    fi
  fi
}

# Get Zone ID
echo -e "${YELLOW}Fetching zone information...${NC}"
ZONE_RESPONSE=$(cf_api GET "/zones?name=$DOMAIN")

if ! echo "$ZONE_RESPONSE" | jq -e '.success' > /dev/null 2>&1; then
  echo -e "${RED}Error: Failed to fetch zone information${NC}"
  echo "$ZONE_RESPONSE" | jq '.'
  exit 1
fi

ZONE_ID=$(echo "$ZONE_RESPONSE" | jq -r '.result[0].id')

if [ -z "$ZONE_ID" ] || [ "$ZONE_ID" = "null" ]; then
  echo -e "${RED}Error: Could not find zone for domain $DOMAIN${NC}"
  exit 1
fi

echo -e "${GREEN}Zone ID: $ZONE_ID${NC}"

case "$MODE" in
  a)
    NEW_IP="${3:-}"
    if [ -z "$RECORD_NAME" ] || [ -z "$NEW_IP" ]; then
      echo -e "${RED}Error: record name and IP are required for 'a' mode${NC}"
      echo "Usage: $0 a <record-name> <ip>"
      exit 1
    fi
    echo -e "${BLUE}Mode: A record${NC}"
    echo -e "${BLUE}Record: ${RECORD_NAME}${NC}"
    echo -e "${BLUE}IP: ${NEW_IP}${NC}"
    
    # Check current DNS record
    CURRENT_RECORD=$(cf_api GET "/zones/$ZONE_ID/dns_records?type=A&name=$RECORD_NAME")
    RECORD_ID=$(echo "$CURRENT_RECORD" | jq -r '.result[0].id // empty')
    CURRENT_IP=$(echo "$CURRENT_RECORD" | jq -r '.result[0].content // empty')

    if [ -n "$RECORD_ID" ] && [ "$RECORD_ID" != "null" ]; then
      echo -e "${BLUE}Current record: ${RECORD_NAME} → ${CURRENT_IP}${NC}"
      
      if [ "$CURRENT_IP" = "$NEW_IP" ]; then
        echo -e "${GREEN}✓ DNS record already points to ${NEW_IP}${NC}"
        exit 0
      fi
      
      # Update existing record
      echo -e "${YELLOW}Updating DNS record...${NC}"
      RESPONSE=$(cf_api PUT "/zones/$ZONE_ID/dns_records/$RECORD_ID" \
        "{\"type\":\"A\",\"name\":\"$RECORD_NAME\",\"content\":\"$NEW_IP\",\"ttl\":1,\"proxied\":false}")
    else
      echo -e "${YELLOW}Creating new DNS record...${NC}"
      RESPONSE=$(cf_api POST "/zones/$ZONE_ID/dns_records" \
        "{\"type\":\"A\",\"name\":\"$RECORD_NAME\",\"content\":\"$NEW_IP\",\"ttl\":1,\"proxied\":false}")
    fi

    if echo "$RESPONSE" | jq -e '.success' > /dev/null 2>&1; then
      RESULT_IP=$(echo "$RESPONSE" | jq -r '.result.content')
      echo -e "${GREEN}✓ Success: ${RECORD_NAME} → ${RESULT_IP}${NC}"
    else
      echo -e "${RED}✗ Failed to update DNS record${NC}"
      echo "$RESPONSE" | jq '.errors'
      exit 1
    fi
    ;;

  cname)
    TARGET="${3:-}"
    if [ -z "$RECORD_NAME" ] || [ -z "$TARGET" ]; then
      echo -e "${RED}Error: record name and target are required for 'cname' mode${NC}"
      echo "Usage: $0 cname <record-name> <target>"
      exit 1
    fi
    echo -e "${BLUE}Mode: CNAME record${NC}"
    echo -e "${BLUE}Record: ${RECORD_NAME}${NC}"
    echo -e "${BLUE}Target: ${TARGET}${NC}"
    
    # Check current DNS record
    CURRENT_RECORD=$(cf_api GET "/zones/$ZONE_ID/dns_records?type=CNAME&name=$RECORD_NAME")
    RECORD_ID=$(echo "$CURRENT_RECORD" | jq -r '.result[0].id // empty')
    CURRENT_TARGET=$(echo "$CURRENT_RECORD" | jq -r '.result[0].content // empty')

    if [ -n "$RECORD_ID" ] && [ "$RECORD_ID" != "null" ]; then
      echo -e "${BLUE}Current record: ${RECORD_NAME} → ${CURRENT_TARGET}${NC}"
      
      if [ "$CURRENT_TARGET" = "$TARGET" ]; then
        echo -e "${GREEN}✓ DNS record already points to ${TARGET}${NC}"
        exit 0
      fi
      
      echo -e "${YELLOW}Updating DNS record...${NC}"
      RESPONSE=$(cf_api PUT "/zones/$ZONE_ID/dns_records/$RECORD_ID" \
        "{\"type\":\"CNAME\",\"name\":\"$RECORD_NAME\",\"content\":\"$TARGET\",\"ttl\":1,\"proxied\":false}")
    else
      echo -e "${YELLOW}Creating new DNS record...${NC}"
      RESPONSE=$(cf_api POST "/zones/$ZONE_ID/dns_records" \
        "{\"type\":\"CNAME\",\"name\":\"$RECORD_NAME\",\"content\":\"$TARGET\",\"ttl\":1,\"proxied\":false}")
    fi

    if echo "$RESPONSE" | jq -e '.success' > /dev/null 2>&1; then
      RESULT_TARGET=$(echo "$RESPONSE" | jq -r '.result.content')
      echo -e "${GREEN}✓ Success: ${RECORD_NAME} → ${RESULT_TARGET}${NC}"
    else
      echo -e "${RED}✗ Failed to update DNS record${NC}"
      echo "$RESPONSE" | jq '.errors'
      exit 1
    fi
    ;;

  lb)
    IP1="${3:-}"
    IP2="${4:-}"
    PROXY="${5:-true}"
    if [ -z "$RECORD_NAME" ] || [ -z "$IP1" ] || [ -z "$IP2" ]; then
      echo -e "${RED}Error: record name and two IPs are required for 'lb' mode${NC}"
      echo "Usage: $0 lb <record-name> <ip1> <ip2> [proxied:true|false]"
      exit 1
    fi
    echo -e "${BLUE}Mode: Load Balancing (round-robin)${NC}"
    echo -e "${BLUE}Record: ${RECORD_NAME}${NC}"
    echo -e "${BLUE}IPs: ${IP1}, ${IP2}${NC}"
    echo -e "${BLUE}Proxied: ${PROXY}${NC}"
    echo ""
    echo -e "${YELLOW}Note: Cloudflare free tier uses round-robin (equal weight)${NC}"
    
    echo -e "${YELLOW}Checking for existing records...${NC}"
    EXISTING=$(cf_api GET "/zones/$ZONE_ID/dns_records?name=$RECORD_NAME")
    EXISTING_IDS=$(echo "$EXISTING" | jq -r '.result[].id' 2>/dev/null || true)
    
    if [ -n "$EXISTING_IDS" ]; then
      echo -e "${YELLOW}Removing existing records...${NC}"
      for id in $EXISTING_IDS; do
        cf_api DELETE "/zones/$ZONE_ID/dns_records/$id" > /dev/null
      done
    fi

    echo -e "${YELLOW}Creating A records (proxied: ${PROXY})...${NC}"
    RESPONSE1=$(cf_api POST "/zones/$ZONE_ID/dns_records" \
      "{\"type\":\"A\",\"name\":\"$RECORD_NAME\",\"content\":\"$IP1\",\"ttl\":1,\"proxied\":${PROXY}}")
    
    if echo "$RESPONSE1" | jq -e '.success' > /dev/null 2>&1; then
      echo -e "${GREEN}✓ Added: ${RECORD_NAME} → ${IP1}${NC}"
    else
      echo -e "${RED}✗ Failed to create A record for ${IP1}${NC}"
      echo "$RESPONSE1" | jq '.errors'
      exit 1
    fi

    RESPONSE2=$(cf_api POST "/zones/$ZONE_ID/dns_records" \
      "{\"type\":\"A\",\"name\":\"$RECORD_NAME\",\"content\":\"$IP2\",\"ttl\":1,\"proxied\":${PROXY}}")
    
    if echo "$RESPONSE2" | jq -e '.success' > /dev/null 2>&1; then
      echo -e "${GREEN}✓ Added: ${RECORD_NAME} → ${IP2}${NC}"
    else
      echo -e "${RED}✗ Failed to create A record for ${IP2}${NC}"
      echo "$RESPONSE2" | jq '.errors'
      exit 1
    fi

    echo ""
    echo -e "${GREEN}✓ Load balancing configured: ${RECORD_NAME} → [${IP1}, ${IP2}] (proxied: ${PROXY})${NC}"
    echo -e "${YELLOW}Verify:${NC}"
    echo "  dig @1.1.1.1 ${RECORD_NAME} +short"
    ;;

  weighted)
    echo -e "${YELLOW}Note: Weighted records require Cloudflare Load Balancing (paid)${NC}"
    echo -e "${YELLOW}Falling back to lb mode (round-robin)${NC}"
    IP1="${3:-}"
    IP2="${4:-}"
    if [ -z "$RECORD_NAME" ] || [ -z "$IP1" ] || [ -z "$IP2" ]; then
      echo -e "${RED}Error: record name and two IPs are required${NC}"
      exit 1
    fi
    # Re-run lb mode
    shift 2
    set -- "lb" "$RECORD_NAME" "$IP1" "$IP2"
    ;;
  *)
    echo -e "${RED}Unknown mode: $MODE${NC}"
    show_usage
    ;;
esac
