#!/usr/bin/env bash
# Read-only freshness check of the encrypted Google Drive backup copy.
# Meant to be wrapped by scripts/run-check-with-alert.sh (ntfy), like
# nats-jetstream-check.sh and waha-session-check.sh. It never calls Google,
# never uploads, and never mutates anything except its own STATE_FILE log.
#
# Signal: backups/omnira_dev/.cloud-last-ok, touched by backup_cloud_sync only
# after a checksum-verified upload. Authentication failures, quota, outages
# and a dead cron all surface the same way: the marker stops advancing.
#
# Output contract (last line): "<utc-timestamp> <OK|WARN|FAIL> reason=<token> ..."
# Exit codes (the wrapper classifies by these): 0 OK or WARN, 1 FAIL.
#   OK    marker younger than WARN_AFTER_HOURS (default 3: the backup runs hourly)
#   WARN  marker older than that but under FAIL_AFTER_HOURS (default 6), or the
#         cloud copy was never configured (local-only is a valid state; WARN
#         never produces an external notification)
#   FAIL  marker older than FAIL_AFTER_HOURS, never succeeded since setup, or a
#         previously working cloud config has disappeared
#
# Usage:
#   scripts/backup-cloud-check.sh
#   BACKUP_DIR=backups/omnira_dev STATE_FILE=/var/log/omnira/backup-cloud-check.log \
#     scripts/backup-cloud-check.sh
set -euo pipefail
cd "$(dirname "$0")/.."

BACKUP_DIR=${BACKUP_DIR:-backups/omnira_dev}
BACKUP_CLOUD_CONF=${BACKUP_CLOUD_CONF:-$HOME/.config/omnira/rclone-backup.conf}
BACKUP_CLOUD_REMOTE=${BACKUP_CLOUD_REMOTE:-omnira-backup}
WARN_AFTER_HOURS=${WARN_AFTER_HOURS:-3}
FAIL_AFTER_HOURS=${FAIL_AFTER_HOURS:-6}
STATE_FILE=${STATE_FILE:-/var/log/omnira/backup-cloud-check.log}
MARKER="$BACKUP_DIR/.cloud-last-ok"

# CHECK_NOW_EPOCH: test-only clock injection; cron never sets it.
now=${CHECK_NOW_EPOCH:-$(date -u +%s)}

emit() { # emit <SEVERITY> <detail...>
  local sev="$1"
  shift
  local line
  line="$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ) $sev $*"
  echo "$line"
  echo "$line" >> "$STATE_FILE" 2>/dev/null || true
}

configured=0
if [ -f "$BACKUP_CLOUD_CONF" ] &&
  RCLONE_CONFIG="$BACKUP_CLOUD_CONF" rclone listremotes 2>/dev/null | grep -qx "${BACKUP_CLOUD_REMOTE}:"; then
  configured=1
fi

if [ "$configured" = 0 ]; then
  if [ -e "$MARKER" ]; then
    emit FAIL "reason=cloud_config_missing_after_success marker=$MARKER"
    exit 1
  fi
  emit WARN "reason=cloud_not_configured local_and_usb_only"
  exit 0
fi

if [ -e "$MARKER" ]; then
  ref=$(stat -c %Y "$MARKER")
  what=cloud_last_ok
else
  ref=$(stat -c %Y "$BACKUP_CLOUD_CONF")
  what=never_succeeded_since_setup
fi
age_s=$((now - ref))
[ "$age_s" -ge 0 ] || age_s=0
age_min=$((age_s / 60))

if [ "$what" = never_succeeded_since_setup ]; then
  if [ "$age_s" -ge $((FAIL_AFTER_HOURS * 3600)) ]; then
    emit FAIL "reason=cloud_never_succeeded age_min=$age_min"
    exit 1
  fi
  emit WARN "reason=cloud_awaiting_first_success age_min=$age_min"
  exit 0
fi

if [ "$age_s" -ge $((FAIL_AFTER_HOURS * 3600)) ]; then
  emit FAIL "reason=cloud_backup_stale age_min=$age_min fail_after_h=$FAIL_AFTER_HOURS"
  exit 1
elif [ "$age_s" -ge $((WARN_AFTER_HOURS * 3600)) ]; then
  emit WARN "reason=cloud_backup_aging age_min=$age_min warn_after_h=$WARN_AFTER_HOURS"
  exit 0
fi
emit OK "reason=cloud_backup_fresh age_min=$age_min"
exit 0
