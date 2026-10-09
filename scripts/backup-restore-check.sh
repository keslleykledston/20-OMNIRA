#!/usr/bin/env bash
# Backup/restore validation with evidence (RTO measured, integrity by checksum, RLS proven AFTER restore).
#   1. builds a throwaway source DB (migrations + fixtures + extra rows incl. an encrypted credential)
#   2. pg_dump -Fc, restore into (a) a new DB of the same cluster and (b) a FRESH cluster (disaster recovery:
#      roles are restored from `pg_dumpall --roles-only`, since a per-database dump does not carry roles)
#   3. compares row checksums of the critical tables, migration ledger, RLS/FORCE flags, policies, RLS helper
#      functions and triggers between source and restores
#   4. proves tenant isolation on each restore as the application role omnira_app (behavioral, not just flags)
# Requires: docker + a running postgres container (default omnira-postgres). Nothing outside throwaway DBs/containers is touched.
#   PG_CONTAINER=omnira-postgres scripts/backup-restore-check.sh
set -uo pipefail
cd "$(dirname "$0")/.."
SRC=${PG_CONTAINER:-omnira-postgres}; OWNER=${PG_OWNER:-omnira}
SRC_DB=omnira_bkp_src; RST_DB=omnira_bkp_restored; FRESH=omnira-bkp-fresh
TABLES="tenants users roles memberships contacts channel_connections channel_credentials conversations messages tickets assignment_events audit_events queues queue_members channel_webhook_events outbox_events"
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'; FAILS=0
ok()  { echo "  ${GRN}✓${NC} $*"; }
bad() { echo "  ${RED}✗ $*${NC}"; FAILS=$((FAILS+1)); }
psql_src()  { docker exec -i "$SRC" psql -U $OWNER -v ON_ERROR_STOP=1 -q -tA "$@"; }
psql_frs()  { docker exec -i "$FRESH" psql -U $OWNER -v ON_ERROR_STOP=1 -q -tA "$@"; }
cleanup() { docker rm -fv $FRESH >/dev/null 2>&1; psql_src -d postgres -c "DROP DATABASE IF EXISTS $SRC_DB" -c "DROP DATABASE IF EXISTS $RST_DB" >/dev/null 2>&1; rm -f /tmp/bkp-*.dump /tmp/bkp-roles.sql; }
trap cleanup EXIT
docker ps --format '{{.Names}}' | grep -qx "$SRC" || { echo "container $SRC not running"; exit 2; }

echo "== 1. source database with data"
psql_src -d postgres -c "DROP DATABASE IF EXISTS $SRC_DB" -c "CREATE DATABASE $SRC_DB" >/dev/null || exit 2
for f in migrations/*.up.sql; do psql_src -d $SRC_DB < "$f" >/dev/null 2>/tmp/bkp.err || { cat /tmp/bkp.err; echo "migration $f failed"; exit 2; }; done
psql_src -d $SRC_DB < web/e2e/fixtures.sql >/dev/null || exit 2
psql_src -d $SRC_DB <<'SQL' >/dev/null || exit 2
-- an encrypted credential (opaque bytes), an audit event, an outbound message, a queued outbox job
INSERT INTO channel_credentials(id,tenant_id,connection_id,ciphertext)
  VALUES ('c1c1c1c1-0000-0000-0000-000000000001','11111111-1111-1111-1111-111111111111','c0000000-0000-0000-0000-00000000c001', decode('deadbeef00112233445566778899aabbccddeeff','hex'));
INSERT INTO audit_events(tenant_id,actor_id,action,resource_type,resource_id,outcome)
  VALUES ('11111111-1111-1111-1111-111111111111','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','conversation.assigned','conversation','d0d0d0d0-0000-0000-0000-000000000001','success');
INSERT INTO messages(tenant_id,conversation_id,channel_connection_id,direction,message_type,body,status,idempotency_key,request_hash,sent_by_user_id)
  VALUES ('11111111-1111-1111-1111-111111111111','d0d0d0d0-0000-0000-0000-000000000001','c0000000-0000-0000-0000-00000000c001','outbound','text','resposta','queued','bkp-key-0001','h','22222222-2222-2222-2222-222222222222');
INSERT INTO outbox_events(tenant_id,event_type,aggregate_type,aggregate_id,payload)
  VALUES ('11111111-1111-1111-1111-111111111111','job.channel.send_text.v1','message','00000000-0000-0000-0000-00000000000a','{}');
SQL
echo "   source rows: $(for t in tenants conversations messages channel_credentials audit_events; do printf '%s=%s ' $t "$(psql_src -d $SRC_DB -c "select count(*) from $t")"; done)"

fingerprint() { # $1 = psql fn, $2 = db  -> one line per table "name|count|md5"
  for t in $TABLES; do echo "$($1 -d $2 -c "select '$t'||'|'||count(*)||'|'||coalesce(md5(string_agg(x::text,'~' order by x::text)),'-') from $t x")"; done
}
structure() { # $1 = psql fn, $2 = db -> RLS flags, policies, helper functions, triggers, migration ledger
  $1 -d $2 -c "select 'rls|'||count(*) filter (where relrowsecurity)||'|'||count(*) filter (where relforcerowsecurity) from pg_class where relkind='r' and relnamespace='public'::regnamespace and relname in ($(echo $TABLES | sed "s/\([a-z_]*\)/'\1'/g; s/' '/','/g"))"
  $1 -d $2 -c "select 'policies|'||count(*)||'|'||md5(string_agg(tablename||policyname||coalesce(qual,'')||coalesce(with_check,''),'~' order by tablename,policyname)) from pg_policies where schemaname='public'"
  $1 -d $2 -c "select 'fn|'||md5(string_agg(pg_get_functiondef(p.oid),'~' order by p.proname)) from pg_proc p where p.pronamespace='public'::regnamespace and p.proname in ('is_system_admin','current_user_id','has_active_membership','has_active_admin_membership','realtime_emit','messages_realtime','conversations_realtime')"
  $1 -d $2 -c "select 'triggers|'||count(*) from pg_trigger where tgname like '%_realtime_trg'"
  $1 -d $2 -c "select 'migrations|'||count(*) from information_schema.tables where table_name='schema_migrations'"
}
SRC_FP=$(fingerprint psql_src $SRC_DB); SRC_ST=$(structure psql_src $SRC_DB)

echo "== 2. dump"
T0=$(date +%s.%N)
docker exec "$SRC" pg_dump -U $OWNER -Fc -d $SRC_DB -f /tmp/bkp-src.dump || { bad "pg_dump failed"; exit 1; }
docker exec "$SRC" pg_dumpall -U $OWNER --roles-only -f /tmp/bkp-roles.sql || { bad "pg_dumpall roles failed"; exit 1; }
T1=$(date +%s.%N); printf "   dump: %.1fs, %s bytes\n" "$(echo "$T1 - $T0" | bc)" "$(docker exec "$SRC" stat -c %s /tmp/bkp-src.dump)"

# behavioral tenant-isolation proof as omnira_app on a restored DB (function: $1 = host exec fn, $2 = db, $3 = container)
isolation() {
  local c=$1 db=$2 pw
  run() { docker exec -e PGPASSWORD=omnira_app "$c" psql -h 127.0.0.1 -U omnira_app -d "$db" -v ON_ERROR_STOP=1 -q -tA -c "$1"; }
  local a b
  a=$(docker exec -e PGPASSWORD=omnira_app -i "$c" psql -h 127.0.0.1 -U omnira_app -d "$db" -tA <<'SQL'
BEGIN;
SELECT set_config('app.current_user_id','22222222-2222-2222-2222-222222222222',true);
SELECT count(*) FROM conversations WHERE tenant_id='11111111-1111-1111-1111-111111111111';
COMMIT;
SQL
)
  b=$(docker exec -e PGPASSWORD=omnira_app -i "$c" psql -h 127.0.0.1 -U omnira_app -d "$db" -tA <<'SQL'
BEGIN;
SELECT set_config('app.current_user_id','22222222-2222-2222-2222-222222222222',true);
SELECT count(*) FROM conversations WHERE tenant_id='22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
SELECT count(*) FROM messages WHERE tenant_id='22222222-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
COMMIT;
SQL
)
  n=$(docker exec -e PGPASSWORD=omnira_app "$c" psql -h 127.0.0.1 -U omnira_app -d "$db" -tAc "SELECT count(*) FROM conversations")
  a=$(echo "$a" | grep -E '^[0-9]+$' | tr '\n' ' ' | sed 's/ $//'); b=$(echo "$b" | grep -E '^[0-9]+$' | tr '\n' ' ' | sed 's/ $//')
  [ "$a" = "1" ] && ok "member reads own tenant's conversation" || bad "member cannot read own data ($a)"
  [ "$b" = "0 0" ] && ok "same user reads 0 rows of the foreign tenant (RLS enforced after restore)" || bad "cross-tenant leak after restore ($b)"
  [ "$n" = "0" ] && ok "no session => no rows (fail closed)" || bad "rows visible without a tenant session ($n)"
}

compare() { # $1 label, $2 fn, $3 db, $4 container
  local fp st
  fp=$(fingerprint $2 $3); st=$(structure $2 $3)
  [ "$fp" = "$SRC_FP" ] && ok "$1: row counts + checksums of $(echo $TABLES | wc -w) critical tables identical" || { bad "$1: data differs"; diff <(echo "$SRC_FP") <(echo "$fp") | head -6; }
  [ "$st" = "$SRC_ST" ] && ok "$1: RLS/FORCE flags, policies, helper functions, triggers, ledger identical" || { bad "$1: structure differs"; diff <(echo "$SRC_ST") <(echo "$st") | head -6; }
  isolation "$4" "$3"
}

echo "== 3a. restore into a new database of the same cluster"
psql_src -d postgres -c "DROP DATABASE IF EXISTS $RST_DB" -c "CREATE DATABASE $RST_DB" >/dev/null
T0=$(date +%s.%N)
docker exec "$SRC" pg_restore -U $OWNER -d $RST_DB --no-owner /tmp/bkp-src.dump 2>/tmp/bkp-restore.err || true
T1=$(date +%s.%N); printf "   RTO (same cluster): %.1fs\n" "$(echo "$T1 - $T0" | bc)"
grep -v "^$" /tmp/bkp-restore.err | grep -iv "already exists" | head -3
# Self-test hook: MUTATE_RLS=1 disables RLS on the restored copy; the check MUST then fail.
[ "${MUTATE_RLS:-0}" = 1 ] && psql_src -d $RST_DB -c "ALTER TABLE conversations NO FORCE ROW LEVEL SECURITY" -c "ALTER TABLE conversations DISABLE ROW LEVEL SECURITY" >/dev/null
compare "same-cluster" psql_src $RST_DB "$SRC"

echo "== 3b. disaster recovery: restore into a FRESH cluster"
docker rm -fv $FRESH >/dev/null 2>&1
docker run -d --name $FRESH -e POSTGRES_USER=$OWNER -e POSTGRES_PASSWORD=x postgres:16-alpine >/dev/null || exit 1
for i in $(seq 1 30); do docker exec $FRESH pg_isready -U $OWNER >/dev/null 2>&1 && break; sleep 1; done
sleep 2
docker cp "$SRC:/tmp/bkp-src.dump" /tmp/bkp-src.dump; docker cp "$SRC:/tmp/bkp-roles.sql" /tmp/bkp-roles.sql
T0=$(date +%s.%N)
# roles first (omnira_app must exist for the GRANTs), skipping the bootstrap superuser that already exists there
grep -v "^CREATE ROLE $OWNER;\|^ALTER ROLE $OWNER " /tmp/bkp-roles.sql | docker exec -i $FRESH psql -U $OWNER -q >/dev/null 2>&1
psql_frs -d postgres -c "CREATE DATABASE $RST_DB" >/dev/null
docker cp /tmp/bkp-src.dump $FRESH:/tmp/bkp-src.dump
docker exec $FRESH pg_restore -U $OWNER -d $RST_DB --no-owner /tmp/bkp-src.dump 2>/tmp/bkp-restore2.err || true
T1=$(date +%s.%N); printf "   RTO (fresh cluster, incl. roles): %.1fs\n" "$(echo "$T1 - $T0" | bc)"
grep -v "^$" /tmp/bkp-restore2.err | head -3
compare "fresh-cluster" psql_frs $RST_DB "$FRESH"

echo "== result"
[ $FAILS -eq 0 ] && echo "${GRN}BACKUP/RESTORE VALIDATED${NC} (RPO = time since the last dump; schedule dumps to meet the target)" || echo "${RED}$FAILS check(s) failed${NC}"
exit $FAILS
