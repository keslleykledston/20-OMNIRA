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
# Usage:
#   scripts/backup-omnira-db.sh
#   PG_CONTAINER=omnira-postgres PG_OWNER=omnira DB_NAME=omnira_dev \
#     RETENTION_DAYS=7 BACKUP_DIR=backups scripts/backup-omnira-db.sh
set -uo pipefail
cd "$(dirname "$0")/.."

PG_CONTAINER=${PG_CONTAINER:-omnira-postgres}
PG_OWNER=${PG_OWNER:-omnira}
DB_NAME=${DB_NAME:-omnira_dev}
RETENTION_DAYS=${RETENTION_DAYS:-7}
BACKUP_DIR=${BACKUP_DIR:-backups}/${DB_NAME}

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

echo "== done"
