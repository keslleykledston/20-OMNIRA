#!/usr/bin/env bash
# Best-effort off-host copy of the database dumps to Google Drive via rclone.
# Sourced by scripts/backup-omnira-db.sh; also unit-tested by
# scripts/test-backup-cloud.sh against a disposable local rclone backend.
#
# Design rules (same spirit as the USB copy in backup-omnira-db.sh):
#   - NEVER fails the backup: the caller ignores the return value. A non-zero
#     return only means "the cloud copy did not complete" and is always
#     accompanied by a "== cloud copy FAILED" line.
#   - Opt-in by configuration: no config file / no remote => SKIPPED, return 0.
#   - Everything is encrypted client-side (rclone crypt) before it leaves the
#     host. The dumps hold customer contacts and messages.
#   - Uses its OWN rclone config (BACKUP_CLOUD_CONF), never the operator's
#     default ~/.config/rclone/rclone.conf.
#   - Retention runs only after a verified upload, so an outage can never
#     leave the cloud with fewer recent copies than before.
#   - Prints no credential.
#
# Do not add `set -e` here: this file is sourced into a script that runs
# without it and must keep going after a failed step.

: "${BACKUP_CLOUD_CONF:=$HOME/.config/omnira/rclone-backup.conf}"
: "${BACKUP_CLOUD_REMOTE:=omnira-backup}"
: "${BACKUP_CLOUD_PATH:=omnira_dev}"
: "${BACKUP_CLOUD_RETENTION_DAYS:=30}"
: "${BACKUP_CLOUD_TIMEOUT_SECONDS:=900}"
: "${BACKUP_CLOUD_VERIFY_MAX_AGE:=3h}"

# Google Drive answers 403 "Quota exceeded ... Queries per minute" when the OAuth client is the one rclone ships
# (shared with every rclone user) and sometimes even with a private one. That failure is transient by nature, so a
# step that fails ONLY because of rate limiting is retried with a growing pause before it is reported as failed.
# Any other failure is reported immediately. Output of the last attempt goes to the caller as before.
: "${BACKUP_CLOUD_QUOTA_RETRIES:=4}"
: "${BACKUP_CLOUD_RETRY_DELAY:=15}"

_cloud_rc() {
  local sub="$1"
  shift
  local try=1 delay="$BACKUP_CLOUD_RETRY_DELAY" rc out
  out=$(mktemp)
  while :; do
    RCLONE_CONFIG="$BACKUP_CLOUD_CONF" timeout "$BACKUP_CLOUD_TIMEOUT_SECONDS" \
      rclone "$sub" "$@" \
      --contimeout 20s --timeout 60s --retries 2 --low-level-retries 3 >"$out" 2>&1
    rc=$?
    if [ "$rc" -ne 0 ] && [ "$try" -lt "$BACKUP_CLOUD_QUOTA_RETRIES" ] &&
      grep -qiE 'quota exceeded|rateLimitExceeded|userRateLimitExceeded|too many requests' "$out"; then
      sleep "$delay"
      delay=$((delay * 2))
      try=$((try + 1))
      continue
    fi
    break
  done
  cat "$out"
  rm -f "$out"
  return "$rc"
}

# backup_cloud_sync <backup_dir> <db_name>
backup_cloud_sync() {
  local dir="$1" db="$2"
  local dest="${BACKUP_CLOUD_REMOTE}:${BACKUP_CLOUD_PATH}"

  if ! command -v rclone >/dev/null 2>&1; then
    echo "== cloud copy SKIPPED: rclone is not installed"
    return 0
  fi
  if [ ! -f "$BACKUP_CLOUD_CONF" ] ||
    ! RCLONE_CONFIG="$BACKUP_CLOUD_CONF" rclone listremotes 2>/dev/null | grep -qx "${BACKUP_CLOUD_REMOTE}:"; then
    echo "== cloud copy SKIPPED: not configured (see scripts/setup-backup-cloud.sh)"
    return 0
  fi

  local filters=(--include "${db}_*.dump" --include "${db}_*.meta.json")

  if ! _cloud_rc copy "$dir" "$dest" "${filters[@]}" --ignore-existing >/dev/null 2>&1; then
    echo "== cloud copy FAILED: upload did not complete — local backup is still valid" >&2
    return 1
  fi

  if ! _cloud_rc cryptcheck "$dir" "$dest" "${filters[@]}" --one-way --max-age "$BACKUP_CLOUD_VERIFY_MAX_AGE" >/dev/null 2>&1; then
    echo "== cloud copy FAILED: checksum verification of the recent dumps did not pass — local backup is still valid" >&2
    return 1
  fi

  if ! _cloud_rc delete "$dest" "${filters[@]}" --min-age "${BACKUP_CLOUD_RETENTION_DAYS}d" >/dev/null 2>&1; then
    echo "== cloud copy OK (verified) but retention cleanup failed — will retry next run" >&2
  fi

  touch "$dir/.cloud-last-ok" 2>/dev/null || true
  echo "== cloud copy OK (client-side encrypted, checksum-verified): ${BACKUP_CLOUD_REMOTE}:${BACKUP_CLOUD_PATH}"
  return 0
}
