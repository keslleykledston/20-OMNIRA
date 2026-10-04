#!/usr/bin/env bash
# Read-only freshness check of the media backups (see backup-omnira-media.sh). Wrapped by run-check-with-alert.sh.
# Output contract (last line): "<utc-timestamp> <OK|WARN|FAIL> reason=<token> ..."; exit 0 for OK/WARN, 1 for FAIL.
#   - no media files yet                          -> OK  (nothing to protect)
#   - cloud configured, marker older than 6 h     -> FAIL (the only off-host copy is stale)
#   - cloud configured, marker older than 3 h     -> WARN
#   - external marker older than 26 h (disk may be unplugged) -> WARN only
# It never calls Google and never mutates anything except its own STATE_FILE log.
set -euo pipefail
cd "$(dirname "$0")/.."

MEDIA_SRC=${MEDIA_SRC:-/opt/omnira-media/clean}
STATE_DIR=${STATE_DIR:-backups/omnira_media}
BACKUP_CLOUD_CONF=${BACKUP_CLOUD_CONF:-$HOME/.config/omnira/rclone-backup.conf}
BACKUP_CLOUD_REMOTE=${BACKUP_CLOUD_REMOTE:-omnira-backup}
WARN_AFTER_HOURS=${WARN_AFTER_HOURS:-3}
FAIL_AFTER_HOURS=${FAIL_AFTER_HOURS:-6}
EXTERNAL_WARN_AFTER_HOURS=${EXTERNAL_WARN_AFTER_HOURS:-26}
STATE_FILE=${STATE_FILE:-/var/log/omnira/backup-media-check.log}
now=${CHECK_NOW_EPOCH:-$(date -u +%s)}

emit() {
  local sev="$1"
  shift
  local line
  line="$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ) $sev $*"
  echo "$line"
  echo "$line" >> "$STATE_FILE" 2>/dev/null || true
}

if [ ! -d "$MEDIA_SRC" ] || [ -z "$(find "$MEDIA_SRC" -type f -print -quit 2>/dev/null)" ]; then
  emit OK "reason=no_media_to_protect"
  exit 0
fi

age_of() { # age_of <file> -> seconds, or -1 when missing
  if [ -e "$1" ]; then echo $((now - $(stat -c %Y "$1"))); else echo -1; fi
}

configured=0
if [ -f "$BACKUP_CLOUD_CONF" ] && RCLONE_CONFIG="$BACKUP_CLOUD_CONF" rclone listremotes 2>/dev/null | grep -qx "${BACKUP_CLOUD_REMOTE}:"; then
  configured=1
fi

notes=""
ext_age=$(age_of "$STATE_DIR/.external-last-ok")
if [ "$ext_age" -lt 0 ]; then
  notes="$notes external=never"
elif [ "$ext_age" -ge $((EXTERNAL_WARN_AFTER_HOURS * 3600)) ]; then
  notes="$notes external_stale_min=$((ext_age / 60))"
fi

if [ "$configured" = 0 ]; then
  emit WARN "reason=media_cloud_not_configured$notes"
  exit 0
fi
cloud_age=$(age_of "$STATE_DIR/.cloud-last-ok")
if [ "$cloud_age" -lt 0 ]; then
  oldest=$(find "$MEDIA_SRC" -type f -printf '%T@\n' | sort -n | head -1 | cut -d. -f1)
  waited=$((now - oldest))
  if [ "$waited" -ge $((FAIL_AFTER_HOURS * 3600)) ]; then
    emit FAIL "reason=media_cloud_never_succeeded unprotected_min=$((waited / 60))$notes"
    exit 1
  fi
  emit WARN "reason=media_cloud_awaiting_first_success$notes"
  exit 0
fi
if [ "$cloud_age" -ge $((FAIL_AFTER_HOURS * 3600)) ]; then
  emit FAIL "reason=media_cloud_backup_stale age_min=$((cloud_age / 60)) fail_after_h=$FAIL_AFTER_HOURS$notes"
  exit 1
elif [ "$cloud_age" -ge $((WARN_AFTER_HOURS * 3600)) ]; then
  emit WARN "reason=media_cloud_backup_aging age_min=$((cloud_age / 60)) warn_after_h=$WARN_AFTER_HOURS$notes"
  exit 0
fi
emit OK "reason=media_backup_fresh age_min=$((cloud_age / 60))$notes"
exit 0
