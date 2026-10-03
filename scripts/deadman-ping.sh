#!/usr/bin/env bash
# Dead-man switch for the OMNIRA cron/alerting chain.
#
# The ntfy wrapper only alerts while it runs. If cron itself stops, the wrapper
# is broken, or notify.sh dies, nothing alerts and the silence looks like
# health. This script closes that gap: it pings an EXTERNAL watcher (Uptime
# Kuma "Push" monitor, healthchecks.io, or any URL that alerts when pings
# stop) ONLY while every job in the chain is demonstrably alive. If any job
# goes quiet, the ping stops and the watcher raises the alarm on its own.
#
# "Alive" = the job's log file was modified within its allowed age:
#   nats-jetstream-check   every 5 min   -> max 15 min
#   waha-session-check     every 5 min   -> max 15 min
#   backup-cloud-check     every 15 min  -> max 45 min
#   backup (hourly dump)   every 60 min  -> max 90 min
#
# Config (from the environment, or /etc/omnira/notify.env, same file as ntfy):
#   DEADMAN_URL       URL to GET while healthy (contains a secret token: never
#                     logged or printed). Unset => "not configured", exit 0.
#   DEADMAN_FAIL_URL  optional URL to GET immediately when a job is stale
#                     (e.g. healthchecks.io /fail, Kuma ?status=down).
#   DEADMAN_LOG       default /var/log/omnira/deadman.log
#
# Exit: 0 healthy-and-pinged, or not configured; 1 a job is stale; 2 the ping
# itself failed. Never mutates anything but DEADMAN_LOG.
set -uo pipefail
cd "$(dirname "$0")/.."

NOTIFY_ENV_FILE=${NOTIFY_ENV_FILE:-/etc/omnira/notify.env}
if [ -r "$NOTIFY_ENV_FILE" ]; then
  set -a
  # shellcheck source=/dev/null
  . "$NOTIFY_ENV_FILE"
  set +a
fi

DEADMAN_LOG=${DEADMAN_LOG:-/var/log/omnira/deadman.log}
LOG_DIR=${DEADMAN_LOG_DIR:-/var/log/omnira}
BACKUP_LOG=${DEADMAN_BACKUP_LOG:-backups/omnira_dev/backup.log}
NOW=${DEADMAN_NOW_EPOCH:-$(date -u +%s)} # test-only clock injection

# name|path|max_age_minutes
JOBS=(
  "nats-jetstream-check|$LOG_DIR/nats-jetstream-check.log|${DEADMAN_MAX_NATS_MIN:-15}"
  "waha-session-check|$LOG_DIR/waha-session-check.log|${DEADMAN_MAX_WAHA_MIN:-15}"
  "backup-cloud-check|$LOG_DIR/backup-cloud-check.log|${DEADMAN_MAX_CLOUD_MIN:-45}"
  "backup-hourly|$BACKUP_LOG|${DEADMAN_MAX_BACKUP_MIN:-90}"
)

log() { echo "$(date -u -d "@$NOW" +%Y-%m-%dT%H:%M:%SZ) $*" >> "$DEADMAN_LOG" 2>/dev/null || true; }
hit() { # hit <url>: GET with short timeouts; never echoes the URL
  curl -fsS -o /dev/null --connect-timeout 5 --max-time 15 --retry 2 --retry-delay 2 "$1" 2>/dev/null
}

if [ -z "${DEADMAN_URL:-}" ]; then
  log "state=not_configured"
  echo "deadman: DEADMAN_URL not set — nothing pinged"
  exit 0
fi

stale=()
for j in "${JOBS[@]}"; do
  IFS='|' read -r name path max_min <<<"$j"
  if [ ! -e "$path" ]; then
    stale+=("$name:missing")
    continue
  fi
  age_min=$(((NOW - $(stat -c %Y "$path")) / 60))
  [ "$age_min" -le "$max_min" ] || stale+=("$name:${age_min}min>${max_min}min")
done

if [ "${#stale[@]}" -gt 0 ]; then
  log "state=stale jobs=${stale[*]} ping=withheld"
  echo "deadman: stale: ${stale[*]} — ping withheld"
  if [ -n "${DEADMAN_FAIL_URL:-}" ]; then
    hit "$DEADMAN_FAIL_URL" || log "state=fail_ping_failed"
  fi
  exit 1
fi

if hit "$DEADMAN_URL"; then
  log "state=ok ping=sent jobs=${#JOBS[@]}"
  echo "deadman: all ${#JOBS[@]} jobs alive — ping sent"
  exit 0
fi
log "state=ping_failed"
echo "deadman: all jobs alive but the ping FAILED" >&2
exit 2
