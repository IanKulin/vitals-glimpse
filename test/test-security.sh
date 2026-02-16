#!/usr/bin/env bash
#
# test-security.sh — run from the repo root on macOS
# Builds vitals-glimpse in Docker, then tests bind, API key,
# IP allowlist, and rate limiting.
#
set -euo pipefail

IMAGE="vitals-glimpse-test"
CONTAINER="vg-test"
PORT=10321
API_KEY="test-secret-key"
RATE_LIMIT=10  # low limit so we can trigger 429 quickly

RED='\033[0;31m'
GREEN='\033[0;32m'
YELLOW='\033[1;33m'
NC='\033[0m'

pass=0
fail=0

check() {
    local description="$1"
    local expected="$2"
    local actual="$3"
    if [ "$actual" = "$expected" ]; then
        echo -e "  ${GREEN}PASS${NC}  $description (got $actual)"
        pass=$((pass + 1))
    else
        echo -e "  ${RED}FAIL${NC}  $description (expected $expected, got $actual)"
        fail=$((fail + 1))
    fi
}

cleanup() {
    echo ""
    echo "Cleaning up..."
    docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
    docker network rm vg-testnet >/dev/null 2>&1 || true
}
trap cleanup EXIT

# ── Build ────────────────────────────────────────────────────────────
echo -e "${YELLOW}Building Docker image...${NC}"
docker build -t "$IMAGE" -f test/Dockerfile .
echo "Pulling helper image..."
docker pull -q curlimages/curl:latest >/dev/null 2>&1 || true

# ── Create test network ──────────────────────────────────────────────
docker network create vg-testnet >/dev/null 2>&1 || true

# ======================================================================
# Test 1: API Key
# ======================================================================
echo ""
echo -e "${YELLOW}Test 1: API Key Authentication${NC}"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" --network vg-testnet \
    -p "$PORT:$PORT" "$IMAGE" \
    -key "$API_KEY" -ratelimit 0 >/dev/null

sleep 2

# No key — expect 401
code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/vitals")
check "No API key returns 401" "401" "$code"

# Wrong key — expect 401
code=$(curl -s -o /dev/null -w "%{http_code}" -H "X-API-Key: wrong" "http://localhost:$PORT/vitals")
check "Wrong API key returns 401" "401" "$code"

# Correct key — expect 200
code=$(curl -s -o /dev/null -w "%{http_code}" -H "X-API-Key: $API_KEY" "http://localhost:$PORT/vitals")
check "Correct API key returns 200" "200" "$code"

# ======================================================================
# Test 2: Rate Limiting
# ======================================================================
echo ""
echo -e "${YELLOW}Test 2: Rate Limiting${NC}"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" --network vg-testnet \
    -p "$PORT:$PORT" "$IMAGE" \
    -ratelimit "$RATE_LIMIT" >/dev/null

sleep 2

# Wait until we're in the first 15 seconds of a minute window.
# Each request takes ~1s (CPU sampling), so RATE_LIMIT requests ≈ 10s.
# Starting early in the window avoids crossing a minute boundary mid-test.
secs=$(date +%S)
if [ "$secs" -gt 45 ]; then
    wait_for=$((61 - secs))
    echo "  (waiting ${wait_for}s for fresh minute window)"
    sleep "$wait_for"
fi

# Send requests up to the limit — all should be 200
all_ok=true
for i in $(seq 1 "$RATE_LIMIT"); do
    code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/vitals")
    if [ "$code" != "200" ]; then
        all_ok=false
        break
    fi
done
if $all_ok; then
    check "First $RATE_LIMIT requests return 200" "true" "true"
else
    check "First $RATE_LIMIT requests return 200" "true" "false"
fi

# Next request should be 429
code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/vitals")
check "Request after limit returns 429" "429" "$code"

# ======================================================================
# Test 3: IP Allowlist
# ======================================================================
echo ""
echo -e "${YELLOW}Test 3: IP Allowlist${NC}"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

# Allow only 192.168.99.0/24 — the host won't be in that range
docker run -d --name "$CONTAINER" --network vg-testnet \
    -p "$PORT:$PORT" "$IMAGE" \
    -allow "192.168.99.0/24" -ratelimit 0 >/dev/null

sleep 2

# Host IP is not in allowlist — expect 403
code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/vitals")
check "Request from non-allowed IP returns 403" "403" "$code"

# Now restart allowing Docker's bridge range (172.16.0.0/12 covers default Docker networks)
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true

# Run a curl from inside the Docker network to test allowed IP
docker run -d --name "$CONTAINER" --network vg-testnet \
    "$IMAGE" \
    -allow "172.16.0.0/12" -ratelimit 0 >/dev/null

sleep 2

# Curl from another container on the same network
code=$(docker run --rm --network vg-testnet curlimages/curl:latest \
    -s -o /dev/null -w "%{http_code}" "http://$CONTAINER:$PORT/vitals")
check "Request from allowed IP returns 200" "200" "$code"

# ======================================================================
# Test 4: No security flags (open access)
# ======================================================================
echo ""
echo -e "${YELLOW}Test 4: Open Access (no security flags)${NC}"
docker rm -f "$CONTAINER" >/dev/null 2>&1 || true
docker run -d --name "$CONTAINER" --network vg-testnet \
    -p "$PORT:$PORT" "$IMAGE" \
    -ratelimit 0 >/dev/null

sleep 2

code=$(curl -s -o /dev/null -w "%{http_code}" "http://localhost:$PORT/vitals")
check "Open access returns 200" "200" "$code"

# Verify JSON response has expected fields
body=$(curl -s "http://localhost:$PORT/vitals")
if echo "$body" | grep -q '"mem_status"' && echo "$body" | grep -q '"cpu_status"'; then
    check "Response contains expected JSON fields" "true" "true"
else
    check "Response contains expected JSON fields" "true" "false"
fi

# ======================================================================
# Summary
# ======================================================================
echo ""
echo "════════════════════════════════════════"
total=$((pass + fail))
echo -e "  Results: ${GREEN}$pass passed${NC}, ${RED}$fail failed${NC} out of $total tests"
echo "════════════════════════════════════════"

if [ "$fail" -gt 0 ]; then
    exit 1
fi
