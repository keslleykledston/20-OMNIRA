#!/usr/bin/env bash
# Tests for scripts/archive-wa-groups.sh against a disposable Postgres (127.0.0.1 only, test-local
# credentials, labelled like scripts/test-integration.sh). The "external disk" is a plain directory; the
# failures a USB disk really has are injected: not mounted, a copy that fails, a truncated copy, a
# corrupted copy, rows that change during the copy, a second run at the same time, a hostile path.
#
# Usage: scripts/test-archive-wa-groups.sh
set -uo pipefail
cd "$(dirname "$0")/.."

LABEL=com.omnira.integration-test
RUN="$(date -u +%Y%m%dT%H%M%SZ)-$$"
SHORT=$(printf '%s' "$RUN" | md5sum | cut -c1-8)
C="omnira-test-arch-$SHORT"
PGPW=$(head -c32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c24)
WORK=$(mktemp -d "${TMPDIR:-/tmp}/archive-test.XXXXXX")
ARCH="$WORK/external/Backup/omnira_groups"
STAGE="$WORK/staging"
mkdir -p "$ARCH" "$STAGE"

pass=0; fail=0
ok()  { pass=$((pass+1)); echo "  ok   $*"; }
bad() { fail=$((fail+1)); echo "  FAIL $*"; }
check() { # check <description> <command...>
  local d="$1"; shift
  if "$@" >/dev/null 2>&1; then ok "$d"; else bad "$d"; fi
}

cleanup() {
  docker rm -f "$C" >/dev/null 2>&1 || true
}
trap cleanup EXIT

echo "== starting disposable Postgres ($C)"
docker run -d --name "$C" --label "$LABEL=true" --label "$LABEL.run=$RUN" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD="$PGPW" -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
PORT=$(docker port "$C" 5432/tcp | head -1 | cut -d: -f2)
for i in $(seq 1 120); do
  # the image restarts the server once after its init phase: wait for the second "ready"
  [ "$(docker logs "$C" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 0.5
done
PSQL="env PGPASSWORD=$PGPW psql -h 127.0.0.1 -p $PORT -U omnira -d archtest"
adm() { env PGPASSWORD="$PGPW" psql -h 127.0.0.1 -p "$PORT" -U omnira -v ON_ERROR_STOP=1 -q -At "$@"; }
sql() { $PSQL -X -q -v ON_ERROR_STOP=1 -At -c "$1"; }
adm -d postgres -c "CREATE DATABASE archtest" >/dev/null
echo "== applying migrations"
DATABASE_URL="postgres://omnira:$PGPW@127.0.0.1:$PORT/archtest" bash tools/apply-migrations.sh up >/dev/null 2>&1 \
  || { echo "FATAL: migrations failed"; exit 1; }

# ---- fixtures: two tenants, a WAHA connection each, three groups
newid() { cat /proc/sys/kernel/random/uuid; }
TA=$(newid); TB=$(newid); CA=$(newid); CB=$(newid); GA1=$(newid); GA2=$(newid); GB1=$(newid)
sql "INSERT INTO tenants(id,legal_name,status) VALUES ('$TA','A','active'),('$TB','B','active');
     INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities) VALUES
       ('$CA','$TA','whatsapp','waha','unofficial','a','active','[\"text\"]'),('$CB','$TB','whatsapp','waha','unofficial','b','active','[\"text\"]');
     INSERT INTO wa_groups(id,tenant_id,channel_connection_id,provider_group_id,name,enabled) VALUES
       ('$GA1','$TA','$CA','120363000000000001@g.us','Oficial A1',true),
       ('$GA2','$TA','$CA','120363000000000002@g.us','Outro A2',true),
       ('$GB1','$TB','$CB','120363000000000001@g.us','Do B',true);" >/dev/null

msg() { # msg <tenant> <group> <id> <days-old> <body>
  local body=${5//\'/\'\'}
  sql "INSERT INTO wa_group_messages(tenant_id,group_id,provider_message_id,author_name,message_type,body,sent_at)
       VALUES ('$1','$2','$3','Autor','text','$body', now() - interval '$4 days')" >/dev/null
}
count() { sql "$1"; }
run() { # run [VAR=value ...] -> runs the job with the test configuration, output in $WORK/out
  env PSQL="$PSQL" REQUIRE_MOUNTPOINT=0 ARCHIVE_DIR="$ARCH" STAGING_DIR="$STAGE" HOT_DAYS=30 COLD_MONTHS=12 "$@" \
    bash scripts/archive-wa-groups.sh >"$WORK/out" 2>&1
  return $?
}
files() { find "$ARCH" -type f -name '*.ndjson.gz' | wc -l; }

# ======================================================================================================
echo "T1  happy path: old rows archived and removed, recent ones kept, tenants kept apart"
msg "$TA" "$GA1" a1 60 'primeira'
msg "$TA" "$GA1" a2 55 'com "aspas", barra \ e quebra'$'\n''de linha e emoji 😀 ção'
msg "$TA" "$GA1" a3 50 'terceira'
msg "$TA" "$GA1" a4 45 'quarta'
msg "$TA" "$GA1" a5 40 'quinta'
msg "$TA" "$GA1" a6 5 'recente 1'
msg "$TA" "$GA1" a7 1 'recente 2'
msg "$TA" "$GA2" b1 70 'a2 antiga 1'
msg "$TA" "$GA2" b2 65 'a2 antiga 2'
msg "$TB" "$GB1" c1 80 'do tenant B'
msg "$TB" "$GB1" c2 2 'recente de B'
rm -f "$STAGE/.last-ok" 2>/dev/null || true
run; rc=$?
check "exit 0" test "$rc" = 0
check "old rows are gone from the database" test "$(count "SELECT count(*) FROM wa_group_messages WHERE sent_at < now() - interval '30 days'")" = 0
check "the 3 recent rows stay" test "$(count "SELECT count(*) FROM wa_group_messages")" = 3
check "3 batches done" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE status='done'")" = 3
check "3 archive files on the external disk" test "$(files)" = 3
check "marker refreshed" test -e "$STAGE/.last-ok"
check "no staging leftovers" test "$(find "$STAGE" -maxdepth 1 -name '*.ndjson.gz*' | wc -l)" = 0
f1=$(find "$ARCH/$TA/$GA1" -name '*.ndjson.gz' | head -1)
check "A/g1 file holds 5 lines" test "$(gzip -dc "$f1" | wc -l)" = 5
cat > "$WORK/body_check.py" <<'PY'
import gzip, json, os, sys
rows = [json.loads(l) for l in gzip.open(sys.argv[1], 'rt', encoding='utf-8')]
want = 'com "aspas", barra \\ e quebra\nde linha e emoji \U0001F600 ção'
sys.exit(0 if any(r['body'] == want and r['provider_message_id'] == 'a2' for r in rows) else 1)
PY
check "tricky text survives the round trip (quotes, backslash, newline, emoji)" python3 "$WORK/body_check.py" "$f1"
check "a tenant's file never contains another tenant's rows" bash -c "! gzip -dc '$f1' | grep -q '$TB'"
fb=$(find "$ARCH/$TB" -name '*.ndjson.gz' | head -1)
check "B's file holds only B" bash -c "gzip -dc '$fb' | grep -q '$TB' && ! gzip -dc '$fb' | grep -q '$TA'"
check "sha256 recorded matches the file" test "$(count "SELECT sha256 FROM wa_group_archive_batches WHERE tenant_id='$TA' AND group_id='$GA1'")" = "$(sha256sum "$f1" | cut -d' ' -f1)"

echo "T2  running again changes nothing"
before=$(files); run; rc=$?
check "exit 0" test "$rc" = 0
check "no new files" test "$(files)" = "$before"
check "no new batches" test "$(count "SELECT count(*) FROM wa_group_archive_batches")" = 3

echo "T3  external disk missing: nothing is deleted, the work waits"
msg "$TA" "$GA1" d1 50 'espera 1'
msg "$TA" "$GA1" d2 48 'espera 2'
rm -f "$STAGE/.last-ok"
run EXTERNAL_MOUNT="$WORK/not-a-mountpoint" REQUIRE_MOUNTPOINT=1; rc=$?
check "exit 0 (not a failure: the data is safe)" test "$rc" = 0
check "rows still in the database" test "$(count "SELECT count(*) FROM wa_group_messages WHERE provider_message_id IN ('d1','d2')")" = 2
check "batch is pending" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE status='pending'")" = 1
check "marker NOT refreshed (so the alert can fire)" test ! -e "$STAGE/.last-ok"
check "says the disk is unavailable" grep -q "external disk not available" "$WORK/out"

echo "T4  injected copy failures: failing, truncated and corrupted copies delete nothing"
printf '#!/bin/sh\nexit 1\n' > "$WORK/cp-fail.sh"
printf '#!/bin/sh\nhead -c $(( $(stat -c %%s "$1") / 2 )) "$1" > "$2"\n' > "$WORK/cp-trunc.sh"
printf '#!/bin/sh\ncp "$1" "$2"; printf X >> "$2"\n' > "$WORK/cp-corrupt.sh"
for kind in fail trunc corrupt; do
  run ARCHIVE_COPY_CMD="bash $WORK/cp-$kind.sh"; rc=$?
  check "$kind: exit 0, rows kept" test "$rc" = 0 -a "$(count "SELECT count(*) FROM wa_group_messages WHERE provider_message_id IN ('d1','d2')")" = 2
  check "$kind: no .partial or final file left for the batch" test "$(find "$ARCH/$TA/$GA1" -name '*.partial' | wc -l)" = 0
  check "$kind: batch still pending" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE status='pending'")" = 1
done
check "files on the external disk unchanged" test "$(files)" = 3

echo "T5  the external disk comes back: the same batch finishes, nothing is duplicated"
run; rc=$?
check "exit 0" test "$rc" = 0
check "rows archived and removed" test "$(count "SELECT count(*) FROM wa_group_messages WHERE provider_message_id IN ('d1','d2')")" = 0
check "4 files (one per batch)" test "$(files)" = 4
check "no pending batch" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE status='pending'")" = 0
check "marker refreshed" test -e "$STAGE/.last-ok"

echo "T6  rows change during the copy: the delete rolls back and the batch is exported again"
msg "$TA" "$GA2" e1 45 'muda 1'
msg "$TA" "$GA2" e2 44 'muda 2'
msg "$TA" "$GA2" e3 43 'muda 3'
printf '#!/bin/sh\n%s -X -q -c "DELETE FROM wa_group_messages WHERE provider_message_id=%s"\ncp "$1" "$2"\n' "$PSQL" "'e2'" > "$WORK/cp-race.sh"
run ARCHIVE_COPY_CMD="bash $WORK/cp-race.sh"; rc=$?
check "exit 0" test "$rc" = 0
check "nothing deleted by the aborted transaction (e1 and e3 still there)" test "$(count "SELECT count(*) FROM wa_group_messages WHERE provider_message_id IN ('e1','e3')")" = 2
run; rc=$?
check "next run archives what is left" test "$(count "SELECT count(*) FROM wa_group_messages WHERE provider_message_id IN ('e1','e3')")" = 0
last=$(find "$ARCH/$TA/$GA2" -name '*.ndjson.gz' -printf '%T@ %p\n' | sort -n | tail -1 | cut -d' ' -f2)
check "the final file has the 2 surviving rows only" test "$(gzip -dc "$last" | wc -l)" = 2

echo "T7  history deleted in the app while a batch is pending: the batch is closed, no file"
msg "$TA" "$GA1" f1 52 'vai sumir'
run EXTERNAL_MOUNT="$WORK/not-a-mountpoint" REQUIRE_MOUNTPOINT=1 >/dev/null
sql "DELETE FROM wa_group_messages WHERE provider_message_id='f1'" >/dev/null
filesbefore=$(files); run; rc=$?
check "exit 0" test "$rc" = 0
check "no file created for an empty batch" test "$(files)" = "$filesbefore"
check "no pending batch" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE status='pending'")" = 0

echo "T8  Apagar histórico: the group's archived files are removed, other groups and tenants untouched"
sql "UPDATE wa_groups SET archive_purge_requested_at = now() WHERE id='$GA1'" >/dev/null
run EXTERNAL_MOUNT="$WORK/not-a-mountpoint" REQUIRE_MOUNTPOINT=1 >/dev/null
check "with the disk missing the request waits" test "$(count "SELECT count(*) FROM wa_groups WHERE id='$GA1' AND archive_purge_requested_at IS NOT NULL")" = 1
run; rc=$?
check "exit 0" test "$rc" = 0
check "A/g1 files removed" test "$(find "$ARCH/$TA/$GA1" -name '*.ndjson.gz' 2>/dev/null | wc -l)" = 0
check "A/g1 batches expired" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE group_id='$GA1' AND status<>'expired'")" = 0
check "request cleared" test "$(count "SELECT count(*) FROM wa_groups WHERE id='$GA1' AND archive_purge_requested_at IS NOT NULL")" = 0
check "A/g2 files kept" test "$(find "$ARCH/$TA/$GA2" -name '*.ndjson.gz' | wc -l)" -ge 1
check "tenant B file kept" test "$(find "$ARCH/$TB" -name '*.ndjson.gz' | wc -l)" = 1

echo "T9  cold retention: files older than COLD_MONTHS are removed"
msg "$TB" "$GB1" g1 400 'muito antiga'
run COLD_MONTHS=999; rc=$?
check "kept while inside the retention" test "$(find "$ARCH/$TB" -name '*.ndjson.gz' | wc -l)" = 2
run COLD_MONTHS=12; rc=$?
check "only the 400-day-old file was removed (the 80-day one stays)" test "$(find "$ARCH/$TB" -name '*.ndjson.gz' | wc -l)" = 1
check "batches expired, not deleted (the record stays)" test "$(count "SELECT count(*) FROM wa_group_archive_batches WHERE tenant_id='$TB' AND status='expired'")" -ge 1

echo "T10 a hostile path is refused and nothing outside the archive is touched"
touch "$WORK/external/decoy.txt"
BID=$(sql "INSERT INTO wa_group_archive_batches(tenant_id,group_id,status,row_count,period_start,period_end,archive_path)
           VALUES ('$TA','$GA2','done',1, now() - interval '700 days', now() - interval '700 days', '../../decoy.txt') RETURNING id")
run COLD_MONTHS=12; rc=$?
check "exit 1 (an alert-worthy condition)" test "$rc" = 1
check "the decoy file outside the archive is untouched" test -e "$WORK/external/decoy.txt"
check "the hostile batch was not expired" test "$(count "SELECT status FROM wa_group_archive_batches WHERE id='$BID'")" = done
sql "UPDATE wa_group_archive_batches SET status='expired' WHERE id='$BID'" >/dev/null

echo "T11 two runs at once: the second one steps aside"
( exec 8>"$STAGE/.lock"; flock 8; sleep 4 ) &
sleep 1
run; rc=$?
check "exit 0 and says another run is in progress" bash -c "test $rc = 0 && grep -q 'another archive run is in progress' '$WORK/out'"
wait

echo
echo "== $pass passed, $fail failed (work dir: $WORK)"
[ "$fail" = 0 ]
