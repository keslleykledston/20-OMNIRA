#!/usr/bin/env bash
# Restore validation of the REAL backup (not fixtures) with the Hub set: the latest dump of the live database restored into a FRESH throwaway cluster
# (disaster recovery), then
#   1. the migration ledger of the restored copy is the newest migration in the repository;
#   2. the control-plane tables of the Hub (hubs, contracts, grants, memberships, pools) have the same row counts as the live database that was dumped;
#   3. as the application role (SET ROLE omnira_app, non-superuser, FORCE RLS) with a SIGNED session context, built from the key the restored database carries:
#        - a real Hub agent sees exactly the Hub inbox rows an independent oracle (has_active_hub_access) allows, and more than zero;
#        - a stranger sees no conversation and no Hub row; a forged identity without the signed context (mode enforce) sees nothing;
#   4. the session-context mode survived the restore (enforce stays enforce) and the app role cannot bypass RLS.
# Reads the live database only through the dump files already taken by scripts/backup-omnira-db.sh (no new load on production) and, for step 2, `count(*)` of
# five small tables. Everything else runs in the throwaway container, which is removed (volumes included) at the end.
#
# Usage: scripts/backup-restore-hub-check.sh   (DUMP=backups/omnira_dev/<file>.dump to choose one; LIVE=omnira-postgres)
set -uo pipefail
cd "$(dirname "$0")/.."
LIVE=${LIVE:-omnira-postgres}; OWNER=${PG_OWNER:-omnira}; DB=${DB_NAME:-omnira_dev}
DUMP=${DUMP:-$(ls -t backups/omnira_dev/omnira_dev_*.dump 2>/dev/null | head -1)}
FRESH=omnira-hubrestore-$$
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'; FAILS=0
ok()  { echo "  ${GRN}✓${NC} $*"; }
bad() { echo "  ${RED}✗ $*${NC}"; FAILS=$((FAILS+1)); }
WORK=$(mktemp -d)
cleanup() { docker rm -fv "$FRESH" >/dev/null 2>&1; shred -u "$WORK"/* 2>/dev/null; rm -rf "$WORK"; }
trap cleanup EXIT
[ -n "$DUMP" ] && [ -f "$DUMP" ] || { echo "no dump found (run scripts/backup-omnira-db.sh)"; exit 2; }
echo "== dump: $DUMP ($(stat -c %s "$DUMP") bytes, sha256 $(sha256sum "$DUMP" | cut -c1-16)…)"
own() { docker exec -i "$FRESH" psql -U "$OWNER" -d "$DB" -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== 1. restore into a FRESH cluster"
docker exec "$LIVE" pg_dumpall -U "$OWNER" --roles-only > "$WORK/roles.sql" || { bad "pg_dumpall roles failed"; exit 1; }
docker run -d --name "$FRESH" -e POSTGRES_USER="$OWNER" -e POSTGRES_PASSWORD=x postgres:16-alpine >/dev/null || exit 1
for i in $(seq 1 120); do [ "$(docker logs "$FRESH" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 0.5; done
docker exec -i "$FRESH" psql -U "$OWNER" -d postgres -X -q < "$WORK/roles.sql" >/dev/null 2>&1   # "role already exists" for the bootstrap superuser is expected
docker exec "$FRESH" psql -U "$OWNER" -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null || exit 1
T0=$(date +%s.%N)
docker exec -i "$FRESH" pg_restore -U "$OWNER" -d "$DB" --no-owner < "$DUMP" 2> "$WORK/restore.err"
T1=$(date +%s.%N); printf "   RTO (fresh cluster): %.1fs\n" "$(echo "$T1 - $T0" | bc)"
if grep -qiE "^pg_restore: error" "$WORK/restore.err"; then bad "pg_restore reported errors: $(grep -iE '^pg_restore: error' "$WORK/restore.err" | head -2)"; else ok "pg_restore finished without errors"; fi

# Self-test hook: MUTATE_RLS=1 switches RLS off on the restored copy of hub_inbox_items; the check MUST then fail (it is not vacuous).
[ "${MUTATE_RLS:-0}" = 1 ] && own -c "ALTER TABLE hub_inbox_items NO FORCE ROW LEVEL SECURITY" -c "ALTER TABLE hub_inbox_items DISABLE ROW LEVEL SECURITY" >/dev/null

echo "== 2. ledger and control-plane rows"
newest=$(ls migrations/*.up.sql | sort | tail -1 | xargs basename | sed 's/.up.sql//')
got=$(own -c "SELECT max(version) FROM schema_migrations")
[ "$got" = "$newest" ] && ok "ledger of the restored copy = newest migration in the repository ($got)" || bad "ledger $got, repository $newest"
for t in service_hubs hub_memberships hub_tenant_service_contracts effective_access_grants work_pools; do
  r=$(own -c "SELECT count(*) FROM $t"); l=$(docker exec "$LIVE" psql -U "$OWNER" -d "$DB" -X -q -At -c "SELECT count(*) FROM $t")
  [ "$r" = "$l" ] && ok "$t: $r rows (same as live)" || bad "$t: restored $r, live $l"
done
a=$(own -c "SELECT count(*) FROM pg_class WHERE relname IN ('hub_inbox_items','conversations','messages','contacts') AND relforcerowsecurity")
[ "$a" = 4 ] && ok "FORCE RLS on hub_inbox_items, conversations, messages, contacts" || bad "FORCE RLS flags: $a of 4"

echo "== 3. as the application role, with a signed session context"
mode=$(own -c "SELECT mode FROM session_ctx_mode")
[ "$mode" = "enforce" ] && ok "session context mode survived the restore: $mode" || bad "session context mode is $mode"
own -c "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname='omnira_app'" | grep -qx f && ok "omnira_app cannot bypass RLS" || bad "omnira_app can bypass RLS"
agent=$(own -c "SELECT hm.user_id FROM hub_memberships hm JOIN effective_access_grants g ON g.user_id=hm.user_id AND g.hub_id=hm.hub_id WHERE g.status='active' GROUP BY 1 ORDER BY count(*) DESC LIMIT 1")
if [ -z "$agent" ]; then bad "no Hub agent with a grant in the restored copy"; else
probe() { # probe <user uuid> <signed yes|forged> -> "items|oracle|convs"
  local sign="SELECT ctx_challenge() AS nonce \\gset
SELECT ctx_begin(p || '|' || encode(hmac(convert_to(p, 'UTF8'), (SELECT key FROM session_ctx_keys WHERE state = 'current'), 'sha256'), 'hex')) AS ok FROM (SELECT format('b1|%s|0|%s', '$1', :'nonce') AS p) x \\gset"
  [ "$2" = signed ] || sign=""
  own <<SQL | tail -1
BEGIN;
$sign
SET LOCAL ROLE omnira_app;
SELECT set_config('app.current_user_id','$1',true) \\gset
SELECT (SELECT count(*) FROM hub_inbox_items) || '|' ||
       (SELECT count(*) FROM hub_inbox_items i WHERE has_active_hub_access(current_user_id(), i.tenant_id, NULL, i.hub_id, false) AND EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id=i.tenant_id AND c.id=i.conversation_id)) || '|' ||
       (SELECT count(*) FROM conversations);
ROLLBACK;
SQL
}
IFS='|' read -r items oracle convs <<< "$(probe "$agent" signed)"
[ "${items:-0}" -gt 0 ] && [ "$items" = "$oracle" ] && ok "Hub agent: $items inbox rows = the independent oracle ($oracle); $convs conversations readable" || bad "Hub agent: items=$items oracle=$oracle convs=$convs"
IFS='|' read -r items oracle convs <<< "$(probe 00000000-0000-4000-8000-0000000000ff signed)"
[ "$items" = 0 ] && [ "$convs" = 0 ] && ok "a stranger reads 0 inbox rows and 0 conversations" || bad "a stranger reads items=$items convs=$convs"
IFS='|' read -r items oracle convs <<< "$(probe "$agent" forged)"
[ "$items" = 0 ] && [ "$convs" = 0 ] && ok "the same agent with a FORGED identity (no signed context, mode enforce) reads 0 rows" || bad "forged identity reads items=$items convs=$convs"
fi
[ "$FAILS" = 0 ] && { echo "PASS: the real backup restores into a fresh cluster with the Hub set intact and isolation enforced"; exit 0; } || { echo "FAIL: $FAILS check(s) failed"; exit 1; }
