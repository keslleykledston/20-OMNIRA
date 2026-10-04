#!/usr/bin/env bash
# PILOT.1 P0.2: operational backup of the real omnira database.
#
# Produces a timestamped pg_dump -Fc of the live database (same format
# already proven restorable end-to-end by scripts/backup-restore-check.sh),
# verifies the artifact is non-empty, records schema/version metadata
# alongside it, prunes artifacts older than the retention window, and never
# prints a credential.
#
# Destination: backups/<db>/ (git-ignored — see .gitignore) inside this repo,
# on the same host's disk as the database. This is intentionally a single
# local copy, appropriate for LAB / pilot scale; it is NOT the target-state
# PITR/off-host strategy documented in docs/operations/DISASTER-RECOVERY.md.
#
# OPS.DISK.1 follow-up: after the local backup succeeds, best-effort-copies
# the dump+meta pair to an external disk (EXTERNAL_MOUNT, default
# /mnt/omnira-backup-external — a USB drive mounted with `nofail` in
# /etc/fstab). This step NEVER fails the backup: if the external disk isn't
# mounted (unplugged, disconnected), it logs a warning and exits 0 exactly
# as if the step didn't exist. The local copy in BACKUP_DIR remains the
# primary, required artifact — the external copy is a secondary, optional
# off-host-ish insurance copy only.
#
# Off-host: after the USB step, an opt-in encrypted copy goes to Google Drive
# through rclone (scripts/lib/backup-cloud.sh). Same rule: never fails the backup.
#
# Usage:
#   scripts/backup-omnira-db.sh
#   PG_CONTAINER=omnira-postgres PG_OWNER=omnira DB_NAME=omnira_dev \
#     RETENTION_DAYS=7 BACKUP_DIR=backups EXTERNAL_MOUNT=/mnt/omnira-backup-external \
#     scripts/backup-omnira-db.sh
set -uo pipefail
cd "$(dirname "$0")/.."

PG_CONTAINER=${PG_CONTAINER:-omnira-postgres}
PG_OWNER=${PG_OWNER:-omnira}
DB_NAME=${DB_NAME:-omnira_dev}
RETENTION_DAYS=${RETENTION_DAYS:-7}
BACKUP_DIR=${BACKUP_DIR:-backups}/${DB_NAME}
EXTERNAL_MOUNT=${EXTERNAL_MOUNT:-/mnt/omnira-backup-external}
EXTERNAL_BACKUP_DIR=${EXTERNAL_BACKUP_DIR:-$EXTERNAL_MOUNT/Backup/omnira_dev}

if ! docker ps --format '{{.Names}}' | grep -qx "$PG_CONTAINER"; then
  echo "backup FAILED: container $PG_CONTAINER is not running" >&2
  exit 2
fi

mkdir -p "$BACKUP_DIR"
timestamp=$(date -u +%Y%m%dT%H%M%SZ)
dump_file="$BACKUP_DIR/${DB_NAME}_${timestamp}.dump"
meta_file="$BACKUP_DIR/${DB_NAME}_${timestamp}.meta.json"

echo "== backing up $DB_NAME from $PG_CONTAINER -> $dump_file"
if ! docker exec "$PG_CONTAINER" pg_dump -U "$PG_OWNER" -Fc "$DB_NAME" > "$dump_file" 2>/tmp/omnira-backup.err; then
  echo "backup FAILED: pg_dump exited non-zero" >&2
  cat /tmp/omnira-backup.err >&2
  rm -f "$dump_file"
  exit 1
fi

size=$(stat -c%s "$dump_file" 2>/dev/null || stat -f%z "$dump_file")
if [ "$size" -lt 1024 ]; then
  echo "backup FAILED: dump artifact is suspiciously small ($size bytes) — treating as failure, not a valid backup" >&2
  rm -f "$dump_file"
  exit 1
fi

schema_version=$(docker exec "$PG_CONTAINER" psql -U "$PG_OWNER" -d "$DB_NAME" -tA -c \
  "SELECT COALESCE(MAX(version), '') FROM schema_migrations" 2>/dev/null || echo "")

cat > "$meta_file" <<EOF
{
  "database": "$DB_NAME",
  "container": "$PG_CONTAINER",
  "taken_at_utc": "$timestamp",
  "size_bytes": $size,
  "schema_version": "$schema_version",
  "format": "pg_dump -Fc",
  "operator": "${SUDO_USER:-${USER:-unknown}}"
}
EOF

echo "== backup OK: $dump_file ($size bytes), schema_version=$schema_version"

echo "== pruning backups older than ${RETENTION_DAYS}d in $BACKUP_DIR"
find "$BACKUP_DIR" -maxdepth 1 -name "${DB_NAME}_*.dump" -mtime "+${RETENTION_DAYS}" -print -delete
find "$BACKUP_DIR" -maxdepth 1 -name "${DB_NAME}_*.meta.json" -mtime "+${RETENTION_DAYS}" -print -delete

# Best-effort external copy — never affects this script's exit status. Uses
# `mountpoint -q`, not just directory existence: an unplugged/unmounted NTFS
# mount still shows an empty local directory, which would silently look
# "available" without this check.
#
# BACKUP.EXT.1: copies to a .partial name first, verifies size+checksum
# against the local source, and only then atomically renames to the final
# filename. A crash/disconnect mid-copy leaves only a .partial file behind —
# never a file under the final name that a future restore would trust as
# complete. Stale .partial files from an interrupted run are cleaned up
# before each attempt so they never accumulate.
if mountpoint -q "$EXTERNAL_MOUNT" 2>/dev/null; then
  ext_ok=1
  if ! mkdir -p "$EXTERNAL_BACKUP_DIR" 2>/dev/null; then
    ext_ok=0
  fi
  if [ "$ext_ok" = 1 ]; then
    for src in "$dump_file" "$meta_file"; do
      base=$(basename "$src")
      dest="$EXTERNAL_BACKUP_DIR/$base"
      partial="$dest.partial"
      rm -f "$partial"
      if ! cp "$src" "$partial" 2>/dev/null; then
        ext_ok=0
        break
      fi
      src_sum=$(sha256sum "$src" | cut -d' ' -f1)
      partial_sum=$(sha256sum "$partial" | cut -d' ' -f1)
      if [ "$src_sum" != "$partial_sum" ]; then
        ext_ok=0
        rm -f "$partial"
        break
      fi
      if ! mv -f "$partial" "$dest" 2>/dev/null; then
        ext_ok=0
        break
      fi
    done
  fi
  if [ "$ext_ok" = 1 ]; then
    echo "== external copy OK (checksum-verified): $EXTERNAL_BACKUP_DIR/$(basename "$dump_file")"
  else
    echo "== external copy SKIPPED: $EXTERNAL_MOUNT is mounted but the copy failed or failed checksum verification (disk full? permissions? disconnected mid-copy?) — local backup is still valid" >&2
  fi
else
  echo "== external copy SKIPPED: $EXTERNAL_MOUNT is not mounted — local backup is still valid"
fi

# Retention on the external disk: it used to keep every dump forever. 35 days is a little more than
# the cloud copy (30), so a group history deleted in the app really leaves the backups.
# shellcheck source=lib/prune-external.sh
. scripts/lib/prune-external.sh
if mountpoint -q "$EXTERNAL_MOUNT" 2>/dev/null; then
  prune_external_dumps "$EXTERNAL_BACKUP_DIR" "$EXTERNAL_MOUNT" "$DB_NAME" "${EXTERNAL_RETENTION_DAYS:-35}" || true
fi

# Best-effort encrypted copy to Google Drive (opt-in; see scripts/setup-backup-cloud.sh).
# shellcheck source=lib/backup-cloud.sh
. scripts/lib/backup-cloud.sh
backup_cloud_sync "$BACKUP_DIR" "$DB_NAME" || true

echo "== done"
