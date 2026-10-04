#!/usr/bin/env bash
# ADR-0015 G6: hot/cold retention of WhatsApp group messages.
#
# The newest HOT_DAYS stay in Postgres (what the Grupos tab reads). Older rows are exported to compressed
# files on the external disk and deleted from the database ONLY after the copy has been verified. Any
# failure (external disk missing or unmounted, a truncated copy, a crash mid-way) leaves the data exactly
# where it was and is retried on the next run; nothing is ever deleted from the database before it is
# safe on the external disk, and a run can be repeated any number of times.
#
# Steps of a run:
#   1. MARK     rows older than the hot window get an archive batch (per tenant + group).
#   2. COPY     every pending batch (including leftovers of failed runs) is exported to a local staging
#               file, copied to the external disk under a temporary name, verified (size + sha256) and
#               renamed into place.
#   3. DELETE   in ONE transaction the batch is marked done and exactly its rows are removed; a count that
#               differs from the export aborts the transaction and the batch stays pending.
#   4. PURGE    groups whose history was deleted in the app ("Apagar histórico") get their archived files
#               removed here, because only this job can reach the external disk.
#   5. EXPIRE   archive files older than COLD_MONTHS are removed.
#
# It touches STATE marker $STAGING_DIR/.last-ok only when nothing is left pending, which is what
# scripts/archive-wa-groups-check.sh watches. Cross-tenant by nature, like the backup scripts: it runs on
# the host as the database owner, outside the application runtime and its RLS.
#
# Usage (cron, daily):
#   scripts/archive-wa-groups.sh
# Tunables (environment): HOT_DAYS=30 COLD_MONTHS=12 EXTERNAL_MOUNT=/mnt/omnira-backup-external
#   ARCHIVE_DIR=$EXTERNAL_MOUNT/Backup/omnira_groups STAGING_DIR=backups/groups-staging
#   PSQL="docker compose exec -T postgres psql -U omnira -d omnira_dev"
# Test hooks: REQUIRE_MOUNTPOINT=0 (the test "external disk" is a plain directory), ARCHIVE_COPY_CMD (to
#   inject a failing/truncating copy), ARCHIVE_NOW_SQL (a SQL timestamp expression replacing now()).
set -euo pipefail
cd "$(dirname "$0")/.."

HOT_DAYS=${HOT_DAYS:-30}
COLD_MONTHS=${COLD_MONTHS:-12}
EXTERNAL_MOUNT=${EXTERNAL_MOUNT:-/mnt/omnira-backup-external}
ARCHIVE_DIR=${ARCHIVE_DIR:-$EXTERNAL_MOUNT/Backup/omnira_groups}
STAGING_DIR=${STAGING_DIR:-backups/groups-staging}
PSQL=${PSQL:-docker compose exec -T postgres psql -U omnira -d omnira_dev}
REQUIRE_MOUNTPOINT=${REQUIRE_MOUNTPOINT:-1}
ARCHIVE_COPY_CMD=${ARCHIVE_COPY_CMD:-cp}
NOW_SQL=${ARCHIVE_NOW_SQL:-now()}
export PGCLIENTENCODING=UTF8

case "$HOT_DAYS$COLD_MONTHS" in
  *[!0-9]*) echo "HOT_DAYS and COLD_MONTHS must be whole numbers" >&2; exit 2 ;;
esac

log() { echo "$(date +%Y-%m-%dT%H:%M:%S%z) $*"; }
q() { $PSQL -X -q -v ON_ERROR_STOP=1 -At "$@"; }

mkdir -p "$STAGING_DIR"
chmod 700 "$STAGING_DIR" 2>/dev/null || true
exec 9>"$STAGING_DIR/.lock"
if ! flock -n 9; then
  log "another archive run is in progress; nothing to do"
  exit 0
fi

pending=0          # work that could not be finished this run (external disk problems)
failures=0         # unexpected conditions worth an alert

external_ready() {
  if [ "$REQUIRE_MOUNTPOINT" = "1" ] && ! mountpoint -q "$EXTERNAL_MOUNT" 2>/dev/null; then
    return 1
  fi
  mkdir -p "$ARCHIVE_DIR" 2>/dev/null && [ -w "$ARCHIVE_DIR" ]
}

# Only paths this job itself writes: <tenant>/<group>/<YYYY-MM>/<batch>.ndjson.gz (relative, no "..").
valid_rel_path() {
  [[ "$1" =~ ^[0-9a-f-]{36}/[0-9a-f-]{36}/[0-9]{4}-[0-9]{2}/[0-9a-f-]{36}\.ndjson\.gz$ ]]
}

CUTOFF=$(q -c "SELECT ($NOW_SQL) - interval '$HOT_DAYS days'")
log "start: hot window ${HOT_DAYS}d (cutoff $CUTOFF), cold retention ${COLD_MONTHS} months"

# ---- 1. MARK -------------------------------------------------------------------------------------
marked=0
while IFS='|' read -r tenant grp; do
  [ -z "$tenant" ] && continue
  n=$(q -c "
    WITH e AS (
      SELECT id, sent_at FROM wa_group_messages
      WHERE tenant_id='$tenant' AND group_id='$grp' AND archive_batch_id IS NULL AND sent_at < '$CUTOFF'
      FOR UPDATE),
    b AS (
      INSERT INTO wa_group_archive_batches (tenant_id, group_id, row_count, period_start, period_end)
      SELECT '$tenant', '$grp', count(*), min(sent_at), max(sent_at) FROM e HAVING count(*) > 0
      RETURNING id, row_count),
    u AS (
      UPDATE wa_group_messages m SET archive_batch_id = (SELECT id FROM b) FROM e
      WHERE m.id = e.id AND EXISTS (SELECT 1 FROM b) RETURNING 1)
    SELECT coalesce((SELECT row_count FROM b), 0)")
  marked=$((marked + n))
done < <(q -F '|' -c "SELECT DISTINCT tenant_id, group_id FROM wa_group_messages WHERE archive_batch_id IS NULL AND sent_at < '$CUTOFF'")
log "marked $marked row(s) for archiving"

# ---- 2/3. COPY, VERIFY, DELETE -------------------------------------------------------------------
archived=0
while IFS='|' read -r batch tenant grp ym; do
  [ -z "$batch" ] && continue
  stage="$STAGING_DIR/$batch.ndjson.gz"
  rows=$(q -c "SELECT count(*) FROM wa_group_messages WHERE tenant_id='$tenant' AND archive_batch_id='$batch'")
  if [ "$rows" = "0" ]; then
    # every row was removed meanwhile (history deleted in the app): there is nothing left to archive
    q -c "UPDATE wa_group_archive_batches SET status='expired', row_count=0 WHERE id='$batch' AND status='pending'" >/dev/null
    log "batch $batch has no rows left (history deleted); closed"
    continue
  fi

  # Export again on every attempt: a staging file from an earlier failed run is never trusted.
  if ! q -c "
      SELECT row_to_json(x) FROM (
        SELECT m.id, m.tenant_id, m.group_id, g.provider_group_id, g.name AS group_name, m.provider_message_id,
               m.author_jid, m.author_name, m.from_me, m.message_type, m.body, m.sent_at, m.created_at
        FROM wa_group_messages m JOIN wa_groups g ON g.tenant_id = m.tenant_id AND g.id = m.group_id
        WHERE m.tenant_id='$tenant' AND m.archive_batch_id='$batch' ORDER BY m.sent_at, m.id) x" \
      | gzip -n > "$stage.tmp"; then
    log "FAIL batch $batch: export failed"
    unlink "$stage.tmp" 2>/dev/null || true
    failures=$((failures + 1)); pending=$((pending + 1)); continue
  fi
  exported=$(gzip -dc "$stage.tmp" | wc -l)
  if [ "$exported" != "$rows" ]; then
    log "FAIL batch $batch: exported $exported line(s) but the batch holds $rows; leaving it pending"
    unlink "$stage.tmp" 2>/dev/null || true
    failures=$((failures + 1)); pending=$((pending + 1)); continue
  fi
  mv -f "$stage.tmp" "$stage"
  sha=$(sha256sum "$stage" | cut -d' ' -f1)
  size=$(stat -c %s "$stage")

  if ! external_ready; then
    log "external disk not available; batch $batch stays pending ($rows row(s) remain in the database)"
    pending=$((pending + 1)); continue
  fi
  rel="$tenant/$grp/$ym/$batch.ndjson.gz"
  valid_rel_path "$rel" || { log "FAIL batch $batch: unexpected path $rel"; failures=$((failures + 1)); pending=$((pending + 1)); continue; }
  dest="$ARCHIVE_DIR/$rel"
  mkdir -p "$(dirname "$dest")"
  if ! $ARCHIVE_COPY_CMD "$stage" "$dest.partial" 2>/dev/null \
      || [ "$(stat -c %s "$dest.partial" 2>/dev/null || echo -1)" != "$size" ] \
      || [ "$(sha256sum "$dest.partial" 2>/dev/null | cut -d' ' -f1)" != "$sha" ]; then
    log "copy of batch $batch failed or did not verify (size/sha256); nothing was deleted, will retry"
    unlink "$dest.partial" 2>/dev/null || true
    pending=$((pending + 1)); continue
  fi
  mv -f "$dest.partial" "$dest"

  # One transaction: mark done and delete exactly the rows that were exported. If the count differs
  # (someone deleted some meanwhile) the whole thing rolls back and the batch is exported again next run.
  if q -c "
      DO \$\$
      DECLARE d integer;
      BEGIN
        DELETE FROM wa_group_messages WHERE tenant_id='$tenant' AND archive_batch_id='$batch';
        GET DIAGNOSTICS d = ROW_COUNT;
        IF d <> $rows THEN
          RAISE EXCEPTION 'archive batch %: deleted % row(s), exported %', '$batch', d, $rows;
        END IF;
        UPDATE wa_group_archive_batches SET status='done', sha256='$sha', archive_path='$rel', copied_at=now(), row_count=$rows
        WHERE id='$batch' AND status='pending';
      END \$\$" >/dev/null 2>&1; then
    unlink "$stage" 2>/dev/null || true
    archived=$((archived + rows))
    log "archived batch $batch: $rows row(s) -> $rel"
  else
    log "batch $batch: the rows changed during the copy; kept in the database, will be exported again"
    pending=$((pending + 1))
  fi
done < <(q -F '|' -c "SELECT id, tenant_id, group_id, to_char(period_start AT TIME ZONE 'UTC','YYYY-MM') FROM wa_group_archive_batches WHERE status='pending' ORDER BY created_at, id")
log "archived $archived row(s)"

# ---- 4. PURGE (history deleted in the app) -------------------------------------------------------
purged=0
while IFS='|' read -r tenant grp requested; do
  [ -z "$tenant" ] && continue
  if ! external_ready; then
    log "external disk not available; archive purge for group $grp postponed"
    pending=$((pending + 1)); continue
  fi
  failed=0
  while IFS='|' read -r batch rel; do
    [ -z "$batch" ] && continue
    if [ -n "$rel" ]; then
      valid_rel_path "$rel" || { log "FAIL purge: unexpected path for batch $batch"; failed=1; continue; }
      unlink "$ARCHIVE_DIR/$rel" 2>/dev/null || [ ! -e "$ARCHIVE_DIR/$rel" ] || { failed=1; continue; }
    fi
    q -c "UPDATE wa_group_archive_batches SET status='expired' WHERE id='$batch'" >/dev/null
    purged=$((purged + 1))
  done < <(q -F '|' -c "SELECT id, archive_path FROM wa_group_archive_batches WHERE tenant_id='$tenant' AND group_id='$grp' AND status='done' AND period_start < '$requested'")
  if [ "$failed" = 0 ]; then
    q -c "UPDATE wa_groups SET archive_purge_requested_at=NULL WHERE tenant_id='$tenant' AND id='$grp' AND archive_purge_requested_at='$requested'" >/dev/null
  else
    failures=$((failures + 1)); pending=$((pending + 1))
  fi
done < <(q -F '|' -c "SELECT tenant_id, id, archive_purge_requested_at FROM wa_groups WHERE archive_purge_requested_at IS NOT NULL")
log "purged $purged archived batch(es) of deleted histories"

# ---- 5. EXPIRE (cold retention) ------------------------------------------------------------------
expired=0
while IFS='|' read -r batch rel; do
  [ -z "$batch" ] && continue
  if ! external_ready; then pending=$((pending + 1)); break; fi
  if [ -n "$rel" ]; then
    valid_rel_path "$rel" || { failures=$((failures + 1)); continue; }
    unlink "$ARCHIVE_DIR/$rel" 2>/dev/null || [ ! -e "$ARCHIVE_DIR/$rel" ] || { failures=$((failures + 1)); continue; }
  fi
  q -c "UPDATE wa_group_archive_batches SET status='expired' WHERE id='$batch'" >/dev/null
  expired=$((expired + 1))
done < <(q -F '|' -c "SELECT id, archive_path FROM wa_group_archive_batches WHERE status='done' AND period_end < ($NOW_SQL) - interval '$COLD_MONTHS months'")
log "expired $expired archived batch(es) older than ${COLD_MONTHS} months"

# ---- result --------------------------------------------------------------------------------------
if [ "$pending" = 0 ] && [ "$failures" = 0 ]; then
  : > "$STAGING_DIR/.last-ok"
  log "OK: nothing left pending"
  exit 0
fi
log "INCOMPLETE: pending=$pending failures=$failures (data is safe; the next run retries)"
[ "$failures" = 0 ] && exit 0
exit 1
