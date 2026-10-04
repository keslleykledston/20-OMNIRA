#!/usr/bin/env bash
# Disposable proof for scripts/backup-omnira-media.sh and scripts/backup-media-check.sh. The external disk is a temp
# directory, the "cloud" is a temp directory behind a real rclone crypt remote; nothing touches the network, the
# operator's rclone config or the real media directory.
set -uo pipefail
cd "$(dirname "$0")/.."
ROOT=$PWD
WORK=$(mktemp -d)
trap 'rm -rf "$WORK"' EXIT

pass=0
fail=0
check() {
  if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass + 1)); else echo "FAIL: $1 (got '$2', want '$3')"; fail=$((fail + 1)); fi
}
contains() { # contains <desc> <haystack> <needle>
  if printf '%s' "$2" | grep -qF -- "$3"; then echo "PASS: $1"; pass=$((pass + 1)); else echo "FAIL: $1 (missing '$3' in: $2)"; fail=$((fail + 1)); fi
}

SRC=$WORK/media/clean
QUAR=$WORK/media/quarantine
EXT=$WORK/usb
STATE=$WORK/state
CLOUD=$WORK/cloud
mkdir -p "$SRC/tenantA" "$SRC/tenantB" "$QUAR/tenantA" "$EXT" "$STATE"
SECRET="CUSTOMER-VOICE-$$"
mkfile() { { printf '%s\n' "$SECRET"; head -c 3000 /dev/urandom; } > "$1"; touch -d "$2" "$1"; }
mkfile "$SRC/tenantA/file-new" "1 hour ago"
mkfile "$SRC/tenantB/file-mid" "40 days ago"
echo "QUARANTINED-UNSCANNED" > "$QUAR/tenantA/never-copy"

REAL_RCLONE=$(command -v rclone)
CONF=$WORK/rclone.conf
cat > "$CONF" <<EOT
[omnira-backup]
type = crypt
remote = $CLOUD
password = $(printf 'pw-one' | "$REAL_RCLONE" obscure -)
password2 = $(printf 'pw-two' | "$REAL_RCLONE" obscure -)
EOT
chmod 600 "$CONF"

run() { # run <extra env...> -- uses the common knobs
  env MEDIA_SRC="$SRC" STATE_DIR="$STATE" EXTERNAL_MOUNT="$EXT" EXTERNAL_MEDIA_DIR="$EXT/Backup/omnira_media" \
    REQUIRE_MOUNTPOINT=0 BACKUP_CLOUD_CONF="$WORK/none.conf" "$@" "$ROOT/scripts/backup-omnira-media.sh" 2>&1
}

echo "=== 1. guards ==="
out=$(env MEDIA_SRC="$WORK/missing" STATE_DIR="$STATE" "$ROOT/scripts/backup-omnira-media.sh" 2>&1); check "missing source exits 0" "$?" "0"
contains "missing source says SKIPPED" "$out" "media backup SKIPPED"
out=$(run MEDIA_BACKUP_RETENTION_DAYS=10); check "retention under 35 days is refused" "$?" "1"
contains "refusal explains why" "$out" "refusing a retention under 35 days"
out=$(run MEDIA_BACKUP_RETENTION_DAYS=abc); check "non-numeric retention is refused" "$?" "1"
out=$(env MEDIA_SRC="$SRC" STATE_DIR="$STATE" EXTERNAL_MOUNT="$WORK/not-a-mountpoint" BACKUP_CLOUD_CONF="$WORK/none.conf" "$ROOT/scripts/backup-omnira-media.sh" 2>&1)
contains "an unmounted external disk is SKIPPED, not an error" "$out" "media external copy SKIPPED"
check "no external marker without a copy" "$([ -e "$STATE/.external-last-ok" ] && echo yes || echo no)" "no"

echo "=== 2. external copy: incremental, verified, quarantine never copied ==="
out=$(run); check "run exits 0" "$?" "0"
contains "external copy reported OK" "$out" "media external copy OK (checksum-verified)"
check "cleared files are copied" "$(find "$EXT/Backup/omnira_media" -type f | wc -l)" "2"
check "quarantine is NOT copied" "$(find "$EXT" -name never-copy | wc -l)" "0"
check "external marker written" "$([ -e "$STATE/.external-last-ok" ] && echo yes || echo no)" "yes"
before=$(stat -c %Y "$EXT/Backup/omnira_media/tenantA/file-new")
sleep 1
out=$(run); contains "second run is a no-op copy" "$out" "media external copy OK"
check "an existing backup file is not rewritten" "$(stat -c %Y "$EXT/Backup/omnira_media/tenantA/file-new")" "$before"

echo "=== 3. a corrupted copy is detected, not trusted ==="
rm -f "$STATE/.external-last-ok"
printf 'tampered' > "$EXT/Backup/omnira_media/tenantA/file-new"
touch -d "$(stat -c %y "$SRC/tenantA/file-new")" "$EXT/Backup/omnira_media/tenantA/file-new"
out=$(run); contains "corruption is reported as FAILED" "$out" "media external copy FAILED"
check "no marker after a failed verification" "$([ -e "$STATE/.external-last-ok" ] && echo yes || echo no)" "no"
cp "$SRC/tenantA/file-new" "$EXT/Backup/omnira_media/tenantA/file-new"

echo "=== 4. retention only removes old BACKUP files, never the source ==="
mkfile "$EXT/Backup/omnira_media/tenantA/ancient" "120 days ago"
out=$(run MEDIA_BACKUP_RETENTION_DAYS=90); contains "old backup file is pruned" "$out" "pruned external media"
check "the ancient backup file is gone" "$([ -e "$EXT/Backup/omnira_media/tenantA/ancient" ] && echo yes || echo no)" "no"
check "a 40-day-old backup file is kept" "$([ -e "$EXT/Backup/omnira_media/tenantB/file-mid" ] && echo yes || echo no)" "yes"
check "source files are untouched" "$(find "$SRC" -type f | wc -l)" "2"
mkdir -p "$WORK/elsewhere/omnira_media"
mkfile "$WORK/elsewhere/omnira_media/ancient-outside" "300 days ago"
out=$(env MEDIA_SRC="$SRC" STATE_DIR="$STATE" EXTERNAL_MOUNT="$EXT" EXTERNAL_MEDIA_DIR="$WORK/elsewhere/omnira_media" REQUIRE_MOUNTPOINT=0 BACKUP_CLOUD_CONF="$WORK/none.conf" "$ROOT/scripts/backup-omnira-media.sh" 2>&1)
contains "a backup directory outside the mount is never pruned" "$out" "is not under"
check "the old file outside the mount is still there" "$([ -e "$WORK/elsewhere/omnira_media/ancient-outside" ] && echo yes || echo no)" "yes"

echo "=== 5. encrypted cloud copy ==="
out=$(run); contains "unconfigured cloud is SKIPPED" "$out" "media cloud copy SKIPPED: not configured"
rm -f "$STATE/.cloud-last-ok"
out=$(run BACKUP_CLOUD_CONF="$CONF" BACKUP_CLOUD_REMOTE=omnira-backup); contains "cloud copy OK" "$out" "media cloud copy OK (client-side encrypted, checksum-verified)"
check "cloud marker written" "$([ -e "$STATE/.cloud-last-ok" ] && echo yes || echo no)" "yes"
check "nothing readable in the cloud: no plaintext content" "$(grep -rl "$SECRET" "$CLOUD" 2>/dev/null | wc -l)" "0"
check "nothing readable in the cloud: no plaintext names" "$(find "$CLOUD" -name 'file-new' -o -name 'tenantA' | wc -l)" "0"
check "cloud holds both files" "$(find "$CLOUD" -type f | wc -l)" "2"
check "no secret in the script output" "$(printf '%s' "$out" | grep -c "$SECRET")" "0"

echo "=== 6. freshness checker ==="
chk() { env MEDIA_SRC="$SRC" STATE_DIR="$STATE" BACKUP_CLOUD_CONF="$1" STATE_FILE="$WORK/check.log" CHECK_NOW_EPOCH="$2" "$ROOT/scripts/backup-media-check.sh" 2>&1; }
now=$(date -u +%s)
out=$(chk "$CONF" "$now"); check "fresh cloud marker is OK" "$?" "0"; contains "OK reason" "$out" "OK reason=media_backup_fresh"
out=$(chk "$CONF" $((now + 4 * 3600))); check "4 h old is WARN (exit 0)" "$?" "0"; contains "WARN aging" "$out" "WARN reason=media_cloud_backup_aging"
out=$(chk "$CONF" $((now + 7 * 3600))); check "7 h old is FAIL (exit 1)" "$?" "1"; contains "FAIL stale" "$out" "FAIL reason=media_cloud_backup_stale"
out=$(chk "$CONF" $((now + 30 * 3600))); contains "stale external disk is mentioned" "$out" "external_stale_min="
out=$(chk "$WORK/none.conf" "$now"); check "unconfigured cloud is only a WARN" "$?" "0"; contains "WARN not configured" "$out" "WARN reason=media_cloud_not_configured"
rm -f "$STATE/.cloud-last-ok"
out=$(chk "$CONF" $((now + 8 * 3600))); check "never-succeeded with old unprotected files is FAIL" "$?" "1"
EMPTY=$WORK/empty; mkdir -p "$EMPTY"
out=$(env MEDIA_SRC="$EMPTY" STATE_DIR="$STATE" STATE_FILE="$WORK/check.log" "$ROOT/scripts/backup-media-check.sh" 2>&1); check "no media means nothing to protect (exit 0)" "$?" "0"; contains "OK no media" "$out" "no_media_to_protect"

echo
echo "passed: $pass  failed: $fail"
[ "$fail" = 0 ]
