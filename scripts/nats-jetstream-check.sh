#!/usr/bin/env bash
# PILOT.4D / PILOT.4D3-C1: read-only NATS JetStream operational check for the
# supervised pilot's outbound send pipeline.
#
# Queries the NATS monitoring HTTP API (host-local only — 127.0.0.1:8222,
# never exposed publicly) for the OMNIRA_JOBS stream and its durable
# consumers. NEVER mutates NATS/JetStream state: no restart, no consumer
# purge, no stream recreate, no message ack/nak, no auto-remediation.
#
# PILOT.4D3-C1 extends the original existence/backlog check with a live
# config-drift check and a capacity/utilization report. The EXPECTED_* values
# below are this script's OWN independent operational assertion of what
# OMNIRA_JOBS' policy should be — cross-referenced against, but never
# generated from, the single application-side source of truth in
# internal/worker/jobsstream (Config()/Ensure()). If the two are ever
# deliberately changed, both must be updated by hand; this script reading
# jobsstream's Go source at runtime would only replace one hazard (drift
# between two independent stream-creation call sites, fixed by PILOT.4D3-C1)
# with another (a monitoring check that can never disagree with the thing it
# is supposed to be checking).
#
# Exits 0 only when:
#   - the monitoring endpoint answers
#   - JetStream is enabled
#   - the expected stream exists, with the expected policy (retention/
#     max_age/max_bytes/discard)
#   - every expected durable consumer exists
#   - stream byte utilization is below the critical threshold
# A consumer having pending/ack-pending/redelivered work is NOT by itself a
# failure — those counts are reported as informational output. This script
# does not invent backlog thresholds for THAT; it only proves the pipeline
# exists, is observable, and is not configured to silently drift or to run
# out of disk.
#
# Usage:
#   scripts/nats-jetstream-check.sh
#   NATS_MONITOR_URL=http://127.0.0.1:8222 STREAM=OMNIRA_JOBS \
#     CONSUMERS="worker-channel-send worker-routing" \
#     STATE_FILE=/var/log/omnira/nats-jetstream-check.log \
#     scripts/nats-jetstream-check.sh
set -euo pipefail

NATS_MONITOR_URL=${NATS_MONITOR_URL:-http://127.0.0.1:8222}
STREAM=${STREAM:-OMNIRA_JOBS}
# CONSUMERS: space-separated durable consumer names expected on $STREAM.
# CONSUMER (singular) is kept as a back-compat override for a single-name
# invocation; if set, it replaces the default list entirely.
CONSUMERS=${CONSUMERS:-${CONSUMER:-"worker-channel-send worker-routing"}}
STATE_FILE=${STATE_FILE:-/var/log/omnira/nats-jetstream-check.log}
TIMEOUT_SECONDS=${TIMEOUT_SECONDS:-10}

# Canonical OMNIRA_JOBS policy (must match internal/worker/jobsstream.Config()).
EXPECTED_RETENTION=${EXPECTED_RETENTION:-limits}
EXPECTED_MAX_AGE_NS=${EXPECTED_MAX_AGE_NS:-604800000000000} # 7 days
EXPECTED_MAX_BYTES=${EXPECTED_MAX_BYTES:-8589934592}        # 8 GiB
EXPECTED_DISCARD=${EXPECTED_DISCARD:-new}

# Utilization thresholds (frozen, PILOT.4D3-C1): below WARN_PCT is silent OK;
# [WARN_PCT, CRIT_PCT) is reported but still exits 0; >= CRIT_PCT exits
# non-zero. No auto-remediation at either threshold — that is PILOT.4E.
WARN_PCT=${WARN_PCT:-70}
CRIT_PCT=${CRIT_PCT:-90}

mkdir -p "$(dirname "$STATE_FILE")" 2>/dev/null || true

fail() {
  echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) FAIL stream=$STREAM consumers=[$CONSUMERS] reason=$1" | tee -a "$STATE_FILE" >&2
  exit 1
}

response=$(curl -sf --max-time "$TIMEOUT_SECONDS" "$NATS_MONITOR_URL/jsz?streams=1&consumers=1&config=1") || fail "monitoring_unreachable"

# set +e around the python invocation: it deliberately exits 3 on a
# CRITICAL utilization finding, and pipefail would otherwise make that
# non-zero status kill the script via set -e before py_status can be read.
set +e
report=$(printf '%s' "$response" | \
  STREAM="$STREAM" CONSUMERS="$CONSUMERS" \
  EXPECTED_RETENTION="$EXPECTED_RETENTION" EXPECTED_MAX_AGE_NS="$EXPECTED_MAX_AGE_NS" \
  EXPECTED_MAX_BYTES="$EXPECTED_MAX_BYTES" EXPECTED_DISCARD="$EXPECTED_DISCARD" \
  WARN_PCT="$WARN_PCT" CRIT_PCT="$CRIT_PCT" \
  python3 -c '
import sys, json, os

stream_name = os.environ["STREAM"]
consumer_names = os.environ["CONSUMERS"].split()
expected_retention = os.environ["EXPECTED_RETENTION"]
expected_max_age_ns = int(os.environ["EXPECTED_MAX_AGE_NS"])
expected_max_bytes = int(os.environ["EXPECTED_MAX_BYTES"])
expected_discard = os.environ["EXPECTED_DISCARD"]
warn_pct = float(os.environ["WARN_PCT"])
crit_pct = float(os.environ["CRIT_PCT"])

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

present = {c.get("name") for c in stream.get("consumer_detail", []) or []}
missing = [c for c in consumer_names if c not in present]
if missing:
    print("FAIL consumer_missing:%s" % ",".join(missing))
    sys.exit(0)

cfg = stream.get("config", {})
drift = []
if cfg.get("retention") != expected_retention:
    drift.append("retention=%s(want %s)" % (cfg.get("retention"), expected_retention))
if cfg.get("max_age") != expected_max_age_ns:
    drift.append("max_age=%s(want %s)" % (cfg.get("max_age"), expected_max_age_ns))
if cfg.get("max_bytes") != expected_max_bytes:
    drift.append("max_bytes=%s(want %s)" % (cfg.get("max_bytes"), expected_max_bytes))
if cfg.get("discard") != expected_discard:
    drift.append("discard=%s(want %s)" % (cfg.get("discard"), expected_discard))
if drift:
    print("FAIL config_drift:%s" % ";".join(drift))
    sys.exit(0)

state = stream.get("state", {})
msgs = state.get("messages", 0)
stream_bytes = state.get("bytes", 0)
max_bytes = cfg.get("max_bytes", 0)
utilization_pct = (stream_bytes / max_bytes * 100.0) if max_bytes and max_bytes > 0 else 0.0

severity = "OK"
if utilization_pct >= crit_pct:
    severity = "CRITICAL"
elif utilization_pct >= warn_pct:
    severity = "WARN"

per_consumer = []
for c in stream.get("consumer_detail", []) or []:
    if c.get("name") not in consumer_names:
        continue
    per_consumer.append("%s(pending=%s,ack_pending=%s,redelivered=%s)" % (
        c.get("name"), c.get("num_pending", "?"), c.get("num_ack_pending", "?"), c.get("num_redelivered", "?")))

line = ("%s stream_messages=%s stream_bytes=%s max_bytes=%s utilization_percent=%.2f consumers=%s" %
        (severity, msgs, stream_bytes, max_bytes, utilization_pct, ",".join(per_consumer)))
print(line)
if severity == "CRITICAL":
    sys.exit(3)
')
py_status=$?
set -e

status_word=${report%% *}
if [ "$status_word" = "FAIL" ]; then
  fail "${report#FAIL }"
fi

if [ "$py_status" -ne 0 ] && [ "$status_word" != "CRITICAL" ]; then
  # Any unexpected non-zero exit that wasn't the deliberate CRITICAL path is
  # itself a failure of this check, not a silent pass.
  fail "internal_error:exit_${py_status}"
fi

echo "$(date -u +%Y-%m-%dT%H:%M:%SZ) $status_word stream=$STREAM ${report#$status_word }" | tee -a "$STATE_FILE"

if [ "$status_word" = "CRITICAL" ]; then
  exit 3
fi
exit 0
