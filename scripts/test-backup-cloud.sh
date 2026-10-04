#!/usr/bin/env bash
# Disposable proof for scripts/lib/backup-cloud.sh and scripts/setup-backup-cloud.sh.
# The "cloud" is a temp directory behind rclone's local backend wrapped in a
# real rclone crypt remote. Nothing here talks to Google, the network, the
# operator's rclone config, or the live database.
#
# Usage: scripts/test-backup-cloud.sh
set -euo pipefail
cd "$(dirname "$0")/.."

WORKDIR=$(mktemp -d)
trap 'rm -rf "$WORKDIR"' EXIT

pass=0
fail=0
check() {
  local desc="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    echo "PASS: $desc"
    pass=$((pass + 1))
  else
    echo "FAIL: $desc (got '$got', want '$want')"
    fail=$((fail + 1))
  fi
}

REAL_RCLONE=$(command -v rclone)
CONF="$WORKDIR/rclone-test.conf"
CLOUD="$WORKDIR/cloud"
BK="$WORKDIR/bk"
mkdir -p "$BK"

cat > "$CONF" <<EOF
[omnira-backup]
type = crypt
remote = $CLOUD
password = $(printf 'pw-one' | "$REAL_RCLONE" obscure -)
password2 = $(printf 'pw-two' | "$REAL_RCLONE" obscure -)
EOF
chmod 600 "$CONF"
rc() { RCLONE_CONFIG="$CONF" "$REAL_RCLONE" "$@"; }

MARKER="SECRET-MARKER-$$"
mk() { # mk <name> <age>
  { printf '%s\n' "$MARKER"; head -c 4000 /dev/urandom; } > "$BK/$1"
  touch -d "$2" "$BK/$1"
}
mk omnira_dev_20261003T100000Z.dump "1 hour ago"
mk omnira_dev_20261003T100000Z.meta.json "1 hour ago"
mk omnira_dev_20260820T100000Z.dump "40 days ago"
mk omnira_dev_20260820T100000Z.meta.json "40 days ago"
FRESH=omnira_dev_20261003T100000Z.dump
OLD=omnira_dev_20260820T100000Z.dump

# shellcheck source=lib/backup-cloud.sh
. scripts/lib/backup-cloud.sh
export BACKUP_CLOUD_REMOTE=omnira-backup BACKUP_CLOUD_PATH=omnira_dev

echo "=== Section 1: not configured => SKIPPED, return 0, nothing created ==="
set +e
out=$(BACKUP_CLOUD_CONF="$WORKDIR/missing.conf" backup_cloud_sync "$BK" omnira_dev 2>&1)
rc1=$?
set -e
check "unconfigured sync returns 0" "$rc1" "0"
check "unconfigured sync says SKIPPED" "$(printf '%s' "$out" | grep -c 'cloud copy SKIPPED: not configured')" "1"
check "unconfigured sync created no cloud data" "$( [ -e "$CLOUD" ] && echo yes || echo no )" "no"

echo "=== Section 2: configured sync uploads, verifies, encrypts, applies retention ==="
export BACKUP_CLOUD_CONF="$CONF"
set +e
out=$(backup_cloud_sync "$BK" omnira_dev 2>&1)
rc2=$?
set -e
check "sync returns 0" "$rc2" "0"
check "sync reports verified OK" "$(printf '%s' "$out" | grep -c 'cloud copy OK (client-side encrypted, checksum-verified)')" "1"
listing=$(rc lsf "omnira-backup:omnira_dev" | sort)
check "remote holds exactly the 2 recent files" "$(printf '%s\n' "$listing" | grep -c .)" "2"
check "recent dump is on the remote" "$(printf '%s\n' "$listing" | grep -cx "$FRESH")" "1"
check "40-day-old dump was removed by retention" "$(printf '%s\n' "$listing" | grep -cx "$OLD")" "0"
check "no readable file name at rest" "$(find "$CLOUD" -type f | grep -c 'dump' || true)" "0"
check "no plaintext content at rest" "$(grep -rl "$MARKER" "$CLOUD" 2>/dev/null | wc -l | tr -d ' ')" "0"
rc copyto "omnira-backup:omnira_dev/$FRESH" "$WORKDIR/restored.dump"
check "restored dump is byte-identical" "$(cmp -s "$WORKDIR/restored.dump" "$BK/$FRESH" && echo same || echo diff)" "same"
check ".cloud-last-ok written" "$( [ -e "$BK/.cloud-last-ok" ] && echo yes || echo no )" "yes"

echo "=== Section 3: second run is idempotent ==="
set +e
backup_cloud_sync "$BK" omnira_dev >/dev/null 2>&1
rc3=$?
set -e
check "repeat sync returns 0" "$rc3" "0"
check "repeat sync keeps 2 files" "$(rc lsf "omnira-backup:omnira_dev" | grep -c .)" "2"

echo "=== Section 4: failed upload => FAILED, return 1, retention NOT applied ==="
mkdir -p "$WORKDIR/seed" "$WORKDIR/shim"
printf 'old-seed\n' > "$WORKDIR/seed/omnira_dev_20260101T000000Z.dump"
touch -d "60 days ago" "$WORKDIR/seed/omnira_dev_20260101T000000Z.dump"
rc copyto "$WORKDIR/seed/omnira_dev_20260101T000000Z.dump" "omnira-backup:omnira_dev/omnira_dev_20260101T000000Z.dump"
check "seeded old remote file exists" "$(rc lsf "omnira-backup:omnira_dev" | grep -c 20260101T000000Z)" "1"
cat > "$WORKDIR/shim/rclone" <<EOF
#!/usr/bin/env bash
[ "\$1" = copy ] && exit 7
exec "$REAL_RCLONE" "\$@"
EOF
chmod +x "$WORKDIR/shim/rclone"
rm -f "$BK/.cloud-last-ok"
set +e
out=$(PATH="$WORKDIR/shim:$PATH" backup_cloud_sync "$BK" omnira_dev 2>&1)
rc4=$?
set -e
check "failed upload returns non-zero" "$( [ "$rc4" -ne 0 ] && echo yes || echo no )" "yes"
check "failed upload says FAILED" "$(printf '%s' "$out" | grep -c 'cloud copy FAILED')" "1"
check "failed upload does not write .cloud-last-ok" "$( [ -e "$BK/.cloud-last-ok" ] && echo yes || echo no )" "no"
check "retention did not run after the failure" "$(rc lsf "omnira-backup:omnira_dev" | grep -c 20260101T000000Z)" "1"
backup_cloud_sync "$BK" omnira_dev >/dev/null 2>&1
check "next successful run applies retention to the seeded file" "$(rc lsf "omnira-backup:omnira_dev" | grep -c 20260101T000000Z)" "0"

echo "=== Section 5: setup script ==="
SETUP_CONF="$WORKDIR/setup/rclone-backup.conf"
TOKEN='{"access_token":"tok-a","token_type":"Bearer","refresh_token":"tok-r","expiry":"2026-01-01T00:00:00Z"}'
printf '%s' "$TOKEN" > "$WORKDIR/token.json"
set +e
setup_out=$(BACKUP_CLOUD_CONF="$SETUP_CONF" scripts/setup-backup-cloud.sh "$WORKDIR/token.json" --no-verify 2>&1)
setup_rc=$?
set -e
check "setup succeeds" "$setup_rc" "0"
check "config mode is 0600" "$(stat -c %a "$SETUP_CONF")" "600"
check "drive scope is drive.file" "$(grep -c '^scope = drive.file$' "$SETUP_CONF")" "1"
check "trash disabled" "$(grep -c '^use_trash = false$' "$SETUP_CONF")" "1"
check "both remotes exist" "$(RCLONE_CONFIG="$SETUP_CONF" "$REAL_RCLONE" listremotes | sort | tr '\n' ' ')" "omnira-backup: omnira-gdrive: "
pw_line=$(grep '^password = ' "$SETUP_CONF" | cut -d' ' -f3)
check "crypt password was generated" "$( [ -n "$pw_line" ] && echo yes || echo no )" "yes"
check "setup output never prints the crypt password" "$(printf '%s' "$setup_out" | grep -c -F "$pw_line")" "0"
before=$(cksum < "$SETUP_CONF")
set +e
BACKUP_CLOUD_CONF="$SETUP_CONF" scripts/setup-backup-cloud.sh "$WORKDIR/token.json" --no-verify >/dev/null 2>&1
again_rc=$?
set -e
check "re-running without --force is refused" "$again_rc" "1"
check "refused run leaves the config untouched" "$(cksum < "$SETUP_CONF")" "$before"
printf 'not a token' > "$WORKDIR/bad.json"
set +e
BACKUP_CLOUD_CONF="$WORKDIR/setup/other.conf" scripts/setup-backup-cloud.sh "$WORKDIR/bad.json" --no-verify >/dev/null 2>&1
bad_rc=$?
set -e
check "malformed token is rejected" "$bad_rc" "1"
check "rejected token creates no config" "$( [ -e "$WORKDIR/setup/other.conf" ] && echo yes || echo no )" "no"

echo "=== Section 5b: Drive rate limiting is retried, other failures are not ==="
STUB="$WORKDIR/stub"; mkdir -p "$STUB"
cat > "$STUB/rclone" <<'STUBEOF'
#!/usr/bin/env bash
# Fake rclone: fails MODE times then succeeds. MODE=quota -> Drive 403 quota text; MODE=other -> a different error.
n=$(cat "$COUNTER" 2>/dev/null || echo 0); n=$((n + 1)); echo "$n" > "$COUNTER"
if [ "$n" -le "${FAILS:-0}" ]; then
  if [ "$MODE" = quota ]; then
    echo "Failed to create file system: googleapi: Error 403: Quota exceeded for quota metric 'Queries' and limit 'Queries per minute'" >&2
  else
    echo "Failed: permission denied" >&2
  fi
  exit 1
fi
echo "ok-from-stub"
exit 0
STUBEOF
chmod +x "$STUB/rclone"
export COUNTER="$WORKDIR/counter"
try_rc() { # try_rc <mode> <fails> -> prints "rc attempts"
  echo 0 > "$COUNTER"
  local rc
  ( PATH="$STUB:$PATH" MODE="$1" FAILS="$2" BACKUP_CLOUD_CONF="$CONF" BACKUP_CLOUD_RETRY_DELAY=0 BACKUP_CLOUD_QUOTA_RETRIES=4 \
    bash -c '. scripts/lib/backup-cloud.sh; _cloud_rc copy a b >/dev/null 2>&1' )
  rc=$?
  echo "$rc $(cat "$COUNTER")"
}
check "a quota error twice, then success: succeeds after 3 attempts" "$(try_rc quota 2)" "0 3"
check "a quota error forever: gives up after the retry budget (4)" "$(try_rc quota 99)" "1 4"
check "any other error is NOT retried" "$(try_rc other 99)" "1 1"
check "no error: one attempt" "$(try_rc quota 0)" "0 1"

echo "=== Section 6: wiring ==="
check "backup script calls the cloud step" "$(grep -c 'backup_cloud_sync "\$BACKUP_DIR" "\$DB_NAME" || true' scripts/backup-omnira-db.sh)" "1"
for f in scripts/backup-omnira-db.sh scripts/lib/backup-cloud.sh scripts/setup-backup-cloud.sh scripts/test-backup-cloud.sh; do
  check "bash -n $f" "$(bash -n "$f" && echo ok)" "ok"
done

echo ""
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
