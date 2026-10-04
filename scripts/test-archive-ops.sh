#!/usr/bin/env bash
# Tests for scripts/archive-wa-groups-check.sh and scripts/lib/prune-external.sh (no database needed).
set -uo pipefail
cd "$(dirname "$0")/.."
WORK=$(mktemp -d "${TMPDIR:-/tmp}/archive-ops.XXXXXX")
pass=0; fail=0
check() { local d="$1"; shift; if "$@" >/dev/null 2>&1; then pass=$((pass+1)); echo "  ok   $d"; else fail=$((fail+1)); echo "  FAIL $d"; fi; }

echo "freshness check"
STAGE="$WORK/staging"; mkdir -p "$STAGE"
chk() { STAGING_DIR="$STAGE" STATE_FILE="$WORK/state.log" CHECK_NOW_EPOCH="$1" bash scripts/archive-wa-groups-check.sh; }
NOW=2000000000
out=$(chk $NOW); rc=$?
check "no marker yet: WARN, exit 0" bash -c "test $rc = 0 && echo '$out' | grep -q 'WARN reason=archive_never_succeeded'"
: > "$STAGE/.last-ok"; touch -d "@$((NOW - 3600))" "$STAGE/.last-ok"
out=$(chk $NOW); rc=$?
check "1 h old: OK" bash -c "test $rc = 0 && echo '$out' | grep -q 'OK reason=archive_fresh'"
touch -d "@$((NOW - 30*3600))" "$STAGE/.last-ok"
out=$(chk $NOW); rc=$?
check "30 h old: WARN, exit 0 (no external alert)" bash -c "test $rc = 0 && echo '$out' | grep -q 'WARN reason=archive_aging'"
touch -d "@$((NOW - 49*3600))" "$STAGE/.last-ok"
out=$(chk $NOW); rc=$?
check "49 h old: FAIL, exit 1 (alert)" bash -c "test $rc = 1 && echo '$out' | grep -q 'FAIL reason=archive_stale'"
check "the log line was appended" grep -q "archive_stale" "$WORK/state.log"

echo "external dump retention"
. scripts/lib/prune-external.sh
MNT="$WORK/mnt"; DIR="$MNT/Backup/omnira_dev"; mkdir -p "$DIR"
old() { touch -d "40 days ago" "$DIR/$1"; }
new() { touch -d "5 days ago" "$DIR/$1"; }
old omnira_dev_20260101T000000Z.dump; old omnira_dev_20260101T000000Z.meta.json; old omnira_dev_20260101T000000Z.dump.partial
new omnira_dev_20260920T000000Z.dump; new omnira_dev_20260920T000000Z.meta.json
old other_db_20260101T000000Z.dump; old notes.txt
prune_external_dumps "$DIR" "$MNT" omnira_dev 35 >/dev/null; rc=$?
check "exit 0" test "$rc" = 0
check "old dump, meta and leftover .partial are removed" bash -c "! ls '$DIR'/omnira_dev_20260101T000000Z* 2>/dev/null | grep -q ."
check "recent dump and meta are kept" bash -c "test -e '$DIR/omnira_dev_20260920T000000Z.dump' && test -e '$DIR/omnira_dev_20260920T000000Z.meta.json'"
check "another database's dump and unrelated files are never touched" bash -c "test -e '$DIR/other_db_20260101T000000Z.dump' && test -e '$DIR/notes.txt'"
check "a retention under 7 days is refused" bash -c ". scripts/lib/prune-external.sh; ! prune_external_dumps '$DIR' '$MNT' omnira_dev 3 2>/dev/null"
check "a non-numeric retention is refused" bash -c ". scripts/lib/prune-external.sh; ! prune_external_dumps '$DIR' '$MNT' omnira_dev '1;x' 2>/dev/null"
check "a directory outside the mount point is refused" bash -c ". scripts/lib/prune-external.sh; mkdir -p '$WORK/elsewhere'; ! prune_external_dumps '$WORK/elsewhere' '$MNT' omnira_dev 35 2>/dev/null"
check "a missing directory is a no-op" bash -c ". scripts/lib/prune-external.sh; prune_external_dumps '$MNT/nope' '$MNT' omnira_dev 35"

echo; echo "== $pass passed, $fail failed"
[ "$fail" = 0 ]
