#!/bin/bash
# Health check for ACPBridge that tests the model end-to-end
# Returns 0 if healthy, 1 if not
# Usage: ./healthcheck.sh [endpoint_url] [api_key]

set -e

BASE_URL="${1:-http://localhost:9090}"
API_KEY="${2:-eu2-8a9f-1c3b-4d5e-9f0a-123456789abc}"

# Step 1: Basic server health check
echo "=== Step 1: Basic health ==="
HEALTH=$(curl -s --max-time 5 "${BASE_URL}/health" 2>/dev/null)
if ! echo "$HEALTH" | python3 -c "import sys,json; d=json.load(sys.stdin); assert d.get('status') == 'ok'" 2>/dev/null; then
  echo "FAIL: Server health check failed"
  echo "Response: $HEALTH"
  exit 1
fi
echo "PASS: Server is up"

# Step 2: Test model with a simple message
echo ""
echo "=== Step 2: Model test ==="
RESPONSE=$(curl -s --max-time 120 -X POST "${BASE_URL}/v1/chat/completions" \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer ${API_KEY}" \
  -d '{"model":"opencode/deepseek-v4-flash-free","messages":[{"role":"user","content":"hi"}],"stream":false}' 2>/dev/null)

CONTENT=$(echo "$RESPONSE" | python3 -c "
import sys, json
try:
  d = json.load(sys.stdin)
  content = d.get('choices',[{}])[0].get('message',{}).get('content','')
  model = d.get('model','')
  tokens = d.get('usage',{}).get('total_tokens',0)
  print(f'{content}', end='')
except Exception as e:
  print(f'__PARSE_ERROR__: {e}', end='')
" 2>/dev/null)

if echo "$CONTENT" | grep -q "__PARSE_ERROR__"; then
  echo "FAIL: Model response parse failed"
  echo "Raw response: $RESPONSE"
  exit 1
fi

if [ -z "$CONTENT" ]; then
  echo "FAIL: Empty model response"
  exit 1
fi

echo "PASS: Model responded"
echo "  Response: ${CONTENT:0:100}"
echo ""
echo "=== Health check: ALL PASS ==="
exit 0
