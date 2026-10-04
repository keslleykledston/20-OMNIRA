#!/usr/bin/env bash
# Read-only freshness check of the WhatsApp group archive job (scripts/archive-wa-groups.sh).
# Meant to be wrapped by scripts/run-check-with-alert.sh (ntfy), like backup-cloud-check.sh. It never
# touches the database or the external disk; its only write is its own STATE_FILE log.
#
# Signal: $STAGING_DIR/.last-ok, touched by the job only when a run left nothing pending. An unmounted or
# failing external disk, a dead cron, and a crashing job all surface the same way: the marker stops advancing.
#
# Output contract (last line): "<utc-timestamp> <OK|WARN|FAIL> reason=<token> ..."
# Exit codes: 0 OK or WARN, 1 FAIL.
#   OK    marker younger than WARN_AFTER_HOURS (default 26: the job runs daily)
#   WARN  marker older than that but under FAIL_AFTER_HOURS (default 48), or the job never succeeded yet
#   FAIL  marker older than FAIL_AFTER_HOURS
set -euo pipefail
cd "$(dirname "$0")/.."

STAGING_DIR=${STAGING_DIR:-backups/groups-staging}
WARN_AFTER_HOURS=${WARN_AFTER_HOURS:-26}
FAIL_AFTER_HOURS=${FAIL_AFTER_HOURS:-48}
STATE_FILE=${STATE_FILE:-/var/log/omnira/archive-wa-groups-check.log}
MARKER="$STAGING_DIR/.last-ok"
now=${CHECK_NOW_EPOCH:-$(date -u +%s)} # CHECK_NOW_EPOCH: test-only clock injection

emit() {
  local sev="$1"; shift
  local line
  line="$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ) $sev $*"
  echo "$line"
  echo "$line" >> "$STATE_FILE" 2>/dev/null || true
}

if [ ! -e "$MARKER" ]; then
  emit WARN "reason=archive_never_succeeded"
  exit 0
fi
age_s=$((now - $(stat -c %Y "$MARKER")))
[ "$age_s" -ge 0 ] || age_s=0
age_min=$((age_s / 60))
if [ "$age_s" -ge $((FAIL_AFTER_HOURS * 3600)) ]; then
  emit FAIL "reason=archive_stale age_min=$age_min fail_after_h=$FAIL_AFTER_HOURS"
  exit 1
elif [ "$age_s" -ge $((WARN_AFTER_HOURS * 3600)) ]; then
  emit WARN "reason=archive_aging age_min=$age_min warn_after_h=$WARN_AFTER_HOURS"
  exit 0
fi
emit OK "reason=archive_fresh age_min=$age_min"
exit 0
