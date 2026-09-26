#!/usr/bin/env bash
# PILOT.4D: read-only NATS JetStream operational check for the supervised
# pilot's outbound send pipeline.
#
# Queries the NATS monitoring HTTP API (host-local only — 127.0.0.1:8222,
# never exposed publicly) for the OMNIRA_JOBS stream and the
# worker-channel-send durable consumer. NEVER mutates NATS/JetStream state:
# no restart, no consumer purge, no stream recreate, no message ack/nak.
#
# Exits 0 only when:
#   - the monitoring endpoint answers
#   - JetStream is enabled
#   - the expected stream exists
#   - the expected durable consumer exists
# A consumer having pending/ack-pending/redelivered work is NOT by itself a
# failure — those counts are reported as informational output. This script
# does not invent backlog thresholds; it only proves the pipeline exists and
# is observable.
#
# Usage:
#   scripts/nats-jetstream-check.sh
#   NATS_MONITOR_URL=http://127.0.0.1:8222 STREAM=OMNIRA_JOBS \
#     CONSUMER=worker-channel-send STATE_FILE=/var/log/omnira/nats-jetstream-check.log \
#     scripts/nats-jetstream-check.sh
set -euo pipefail

NATS_MONITOR_URL=${NATS_MONITOR_URL:-http://127.0.0.1:8222}
STREAM=${STREAM:-OMNIRA_JOBS}
CONSUMER=${CONSUMER:-worker-channel-send}
STATE_FILE=${STATE_FILE:-/var/log/omnira/nats-jetstream-check.log}
TIMEOUT_SECONDS=${TIMEOUT_SECONDS:-10}

mkdir -p "$(dirname "$STATE_FILE")" 2>/dev/null || true

fail() {
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) FAIL stream=$STREAM consumer=$CONSUMER reason=$1" | tee -a "$STATE_FILE" >&2
  exit 1
}

response=$(curl -sf --max-time "$TIMEOUT_SECONDS" "$NATS_MONITOR_URL/jsz?streams=1&consumers=1") || fail "monitoring_unreachable"

report=$(printf '%s' "$response" | STREAM="$STREAM" CONSUMER="$CONSUMER" python3 -c '
import sys, json, os

stream_name = os.environ["STREAM"]
consumer_name = os.environ["CONSUMER"]

try:
    data = json.load(sys.stdin)
except Exception:
    print("FAIL malformed_response")
    sys.exit(0)

if not data.get("config") or "streams" not in data:
    print("FAIL jetstream_unavailable")
    sys.exit(0)

stream = None
for acc in data.get("account_details", []) or []:
    for s in acc.get("stream_detail", []) or []:
        if s.get("name") == stream_name:
            stream = s
            break

if stream is None:
    print("FAIL stream_missing")
    sys.exit(0)

consumer = None
for c in stream.get("consumer_detail", []) or []:
    if c.get("name") == consumer_name:
        consumer = c
        break

if consumer is None:
    print("FAIL consumer_missing")
    sys.exit(0)

state = stream.get("state", {})
msgs = state.get("messages", "?")
pending = consumer.get("num_pending", "?")
ack_pending = consumer.get("num_ack_pending", "?")
redelivered = consumer.get("num_redelivered", "?")
print("OK stream_messages=%s pending=%s ack_pending=%s redelivered=%s" % (msgs, pending, ack_pending, redelivered))
')

status_word=${report%% *}
if [ "$status_word" = "FAIL" ]; then
  fail "${report#FAIL }"
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) OK stream=$STREAM consumer=$CONSUMER ${report#OK }" | tee -a "$STATE_FILE"
