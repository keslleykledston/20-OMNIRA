#!/usr/bin/env bash
# PILOT.4C: read-only WAHA session status check for the supervised pilot.
#
# Exits 0 when the configured pilot session reports the expected status
# (default: WORKING), non-zero otherwise. This script NEVER mutates the WAHA
# session: no restart, no reconnect, no QR generation, no auto-remediation —
# it only calls GET /api/sessions/{name}, the same read-only endpoint OMNIRA's
# own API uses (internal/channels/adapters/waha/session_controller.go).
#
# It does not send an alert by itself — see docs/operations/PILOT-RUNBOOK.md
# for how an operator or cron wrapper should watch STATE_FILE. A local log is
# visibility, not an active notification.
#
# Usage:
#   scripts/waha-session-check.sh
#   WAHA_SESSION=omnira_<connection-id> scripts/waha-session-check.sh
#   WAHA_CONTAINER=20-omnira-waha-1 WAHA_URL=http://waha:3000 \
#     WAHA_SESSION=omnira_<connection-id> STATE_FILE=/var/log/omnira/waha-session-check.log \
#     scripts/waha-session-check.sh
#
# Must run where WAHA_URL is reachable (inside the omnira-network, e.g. via
# `docker run --network <net> ...` or from a container already on it) unless
# WAHA_URL is overridden to a host-reachable address.
set -euo pipefail

WAHA_CONTAINER=${WAHA_CONTAINER:-20-omnira-waha-1}
WAHA_URL=${WAHA_URL:-http://waha:3000}
WAHA_SESSION=${WAHA_SESSION:?set WAHA_SESSION to the pilot session name, e.g. omnira_<connection-id>}
EXPECTED_STATUS=${EXPECTED_STATUS:-WORKING}
STATE_FILE=${STATE_FILE:-/var/log/omnira/waha-session-check.log}
TIMEOUT_SECONDS=${TIMEOUT_SECONDS:-10}

mkdir -p "$(dirname "$STATE_FILE")" 2>/dev/null || true

fail() {
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) FAIL session=$WAHA_SESSION expected=$EXPECTED_STATUS reason=$1" | tee -a "$STATE_FILE" >&2
  exit 1
}

if ! docker ps --format '{{.Names}}' | grep -qx "$WAHA_CONTAINER"; then
  fail "container_not_running"
fi

api_key=$(docker exec "$WAHA_CONTAINER" printenv WAHA_API_KEY 2>/dev/null) || fail "cannot_read_api_key"
if [ -z "$api_key" ]; then
  fail "empty_api_key"
fi

response=$(curl -sf --max-time "$TIMEOUT_SECONDS" -H "X-Api-Key: $api_key" \
  "$WAHA_URL/api/sessions/$WAHA_SESSION") || { unset api_key; fail "provider_unreachable"; }
unset api_key

status=$(printf '%s' "$response" | python3 -c '
import sys, json
try:
    print(json.load(sys.stdin).get("status", ""))
except Exception:
    print("")
')

if [ -z "$status" ]; then
  fail "malformed_response"
fi

if [ "$status" != "$EXPECTED_STATUS" ]; then
  fail "status=$status"
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) OK session=$WAHA_SESSION status=$status" | tee -a "$STATE_FILE"
