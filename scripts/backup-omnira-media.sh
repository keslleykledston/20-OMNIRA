#!/usr/bin/env bash
# Backs up the inbound media files that passed the antivirus (ADR-0016): only <media>/clean, never quarantine.
# WAHA deletes its own copy within minutes, so after capture this directory is the ONLY copy of each file; the
# database dump holds only state and hashes. Files never change once cleared, so every copy is incremental
# (--ignore-existing) and verified by checksum.
#
# Destinations (each optional, each independent, none may fail the other):
#   1. external disk  $EXTERNAL_MOUNT/Backup/omnira_media   (rsync; skipped when the disk is not mounted)
#   2. Google Drive   $BACKUP_CLOUD_REMOTE:omnira_media     (rclone crypt, client-side encrypted; same config and
#                                                            library as the database dumps)
# Retention on both: MEDIA_BACKUP_RETENTION_DAYS (default 90 = the 60-day file retention plus 30 days of margin).
# Markers in $STATE_DIR (.external-last-ok / .cloud-last-ok) feed scripts/backup-media-check.sh.
#
# Usage (cron, hourly at :35):
#   cd /path/to/OMNIRA && ./scripts/backup-omnira-media.sh >> backups/omnira_media/backup.log 2>&1
# Test knobs: MEDIA_SRC STATE_DIR EXTERNAL_MOUNT REQUIRE_MOUNTPOINT=0 BACKUP_CLOUD_CONF.
set -uo pipefail
cd "$(dirname "$0")/.."

MEDIA_SRC=${MEDIA_SRC:-/opt/omnira-media/clean}
STATE_DIR=${STATE_DIR:-backups/omnira_media}
EXTERNAL_MOUNT=${EXTERNAL_MOUNT:-/mnt/omnira-backup-external}
EXTERNAL_MEDIA_DIR=${EXTERNAL_MEDIA_DIR:-$EXTERNAL_MOUNT/Backup/omnira_media}
REQUIRE_MOUNTPOINT=${REQUIRE_MOUNTPOINT:-1}
RETENTION_DAYS=${MEDIA_BACKUP_RETENTION_DAYS:-90}
VERIFY_RECENT_DAYS=${MEDIA_BACKUP_VERIFY_DAYS:-3}
export BACKUP_CLOUD_PATH=${BACKUP_CLOUD_PATH_MEDIA:-omnira_media}

log() { echo "$(date +%Y-%m-%dT%H:%M:%S%z) $*"; }

mkdir -p "$STATE_DIR" 2>/dev/null || true
exec 9>"$STATE_DIR/.lock"
flock -n 9 || { log "== media backup SKIPPED: another run is in progress"; exit 0; }

case "$RETENTION_DAYS" in '' | *[!0-9]*) log "== media backup ABORTED: retention '$RETENTION_DAYS' is not a number" >&2; exit 1 ;; esac
if [ "$RETENTION_DAYS" -lt 35 ]; then
  log "== media backup ABORTED: refusing a retention under 35 days (files are kept 60 days)" >&2
  exit 1
fi

if [ ! -d "$MEDIA_SRC" ]; then
  log "== media backup SKIPPED: $MEDIA_SRC does not exist (media pipeline not enabled here)"
  exit 0
fi
count=$(find "$MEDIA_SRC" -type f | wc -l)
log "== media backup start: $count file(s) in $MEDIA_SRC"

# --- 1. external disk ------------------------------------------------------------------------------------------
external_ok=0
mounted=0
if [ "$REQUIRE_MOUNTPOINT" = 1 ]; then mountpoint -q "$EXTERNAL_MOUNT" 2>/dev/null && mounted=1; else [ -d "$EXTERNAL_MOUNT" ] && mounted=1; fi
if [ "$mounted" = 1 ]; then
  RSYNC_OPTS=(-rt --no-perms --no-owner --no-group --ignore-existing)
  if mkdir -p "$EXTERNAL_MEDIA_DIR" 2>/dev/null && rsync "${RSYNC_OPTS[@]}" "$MEDIA_SRC"/ "$EXTERNAL_MEDIA_DIR"/ >/dev/null 2>&1; then
    # Verify by content every file written in the last VERIFY_RECENT_DAYS: a diff here means a bad copy.
    diffs=$(find "$MEDIA_SRC" -type f -mtime "-$VERIFY_RECENT_DAYS" -printf '%P\0' |
      rsync -rnci --no-perms --no-owner --no-group --from0 --files-from=- "$MEDIA_SRC"/ "$EXTERNAL_MEDIA_DIR"/ 2>/dev/null | grep -c '^>f')
    if [ "$diffs" = 0 ]; then
      external_ok=1
      touch "$STATE_DIR/.external-last-ok"
      log "== media external copy OK (checksum-verified): $EXTERNAL_MEDIA_DIR"
    else
      log "== media external copy FAILED: $diffs recent file(s) differ after the copy (disk full? disconnected mid-copy?)" >&2
    fi
  else
    log "== media external copy FAILED: could not write to $EXTERNAL_MEDIA_DIR" >&2
  fi
else
  log "== media external copy SKIPPED: $EXTERNAL_MOUNT is not mounted"
fi

# Retention on the external copy: only inside the media directory, only under the mount point.
if [ "$external_ok" = 1 ]; then
  case "$EXTERNAL_MEDIA_DIR" in
    "$EXTERNAL_MOUNT"/*omnira_media*)
      find "$EXTERNAL_MEDIA_DIR" -type f -mtime "+$RETENTION_DAYS" -print -delete | sed 's/^/== pruned external media: /' | head -50
      find "$EXTERNAL_MEDIA_DIR" -mindepth 1 -type d -empty -delete 2>/dev/null
      ;;
    *) log "== media external prune SKIPPED: $EXTERNAL_MEDIA_DIR is not under $EXTERNAL_MOUNT" >&2 ;;
  esac
fi

# --- 2. encrypted cloud copy -----------------------------------------------------------------------------------
# shellcheck source=lib/backup-cloud.sh
. scripts/lib/backup-cloud.sh
dest="${BACKUP_CLOUD_REMOTE}:${BACKUP_CLOUD_PATH}"
if ! command -v rclone >/dev/null 2>&1; then
  log "== media cloud copy SKIPPED: rclone is not installed"
elif [ ! -f "$BACKUP_CLOUD_CONF" ] || ! RCLONE_CONFIG="$BACKUP_CLOUD_CONF" rclone listremotes 2>/dev/null | grep -qx "${BACKUP_CLOUD_REMOTE}:"; then
  log "== media cloud copy SKIPPED: not configured (see scripts/setup-backup-cloud.sh)"
else
  if ! _cloud_rc copy "$MEDIA_SRC" "$dest" --ignore-existing >/dev/null 2>&1; then
    log "== media cloud copy FAILED: upload did not complete" >&2
  elif ! _cloud_rc cryptcheck "$MEDIA_SRC" "$dest" --one-way --max-age "${VERIFY_RECENT_DAYS}d" >/dev/null 2>&1; then
    log "== media cloud copy FAILED: checksum verification of the recent files did not pass" >&2
  else
    if ! _cloud_rc delete "$dest" --min-age "${RETENTION_DAYS}d" >/dev/null 2>&1; then
      log "== media cloud copy OK (verified) but retention cleanup failed - will retry next run" >&2
    fi
    touch "$STATE_DIR/.cloud-last-ok"
    log "== media cloud copy OK (client-side encrypted, checksum-verified): $dest"
  fi
fi
log "== media backup end"
exit 0
