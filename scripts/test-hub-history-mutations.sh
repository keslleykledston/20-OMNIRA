#!/usr/bin/env bash
# Mutation test for THE DELEGATED MEMORY OF THE CONTACT (ADR-0040 phase 06, migration 117): weakens each rule of the attendance routes, the service and the policies of the
# closure and follow-up tables in turn; the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubhistmut-$$; DB=omnira_test_histmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/attendance/application/service.go internal/attendance/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=histmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestDelegatedAttendanceHistory' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "SURVIVED: $1"; [ -n "${REPORT:-}" ] && return 0; echo "FAIL: mutation survived"; exit 1; fi
  [ "$(echo "$2" | grep -c -- '--- FAIL')" -ge 1 ] || { echo "$2" | tail -12; echo "FAIL: mutation '$1' was inconclusive (no failing test; build error?)"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0
mut() { # mut <name> <file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  python3 - "$WORK/orig" "$2" "$3" "$4" <<'PY'
import sys
orig,f,a,b=sys.argv[1:5]
s=open(orig+"/"+f).read()
assert a in s, "mutation target not found: "+a
open(f,'w').write(s.replace(a,b,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
sqlmut() { # sqlmut <name> <migration file> <from> <to> [all]   (all: replace every occurrence, for a rule written in two layers)
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" "${5:-}" <<'PY'
import sys
p,f,t,every=sys.argv[1:5]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t) if every == "all" else s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
# NOT mutants (equivalent by construction):
#   - the audit text rule: ids, counts and enums only, by construction of the metadata map (the test looks for the text in the trail);
#   - the hub named in the audit row: the audit repository adds acting_as from the context for every action, so the service passes nothing extra.
AS=internal/attendance/application/service.go; AH=internal/attendance/adapters/http.go
M=000117_hub_delegated_attendance_history.up.sql

mut "attendance-history asks a key every delegate has" $AH 'tenancyadapters.Delegable("attendance.history", wrap(h.historyOfContact))' 'tenancyadapters.Delegable("conversation.read", wrap(h.historyOfContact))'
mut "attendance-context asks a key every delegate has" $AH 'tenancyadapters.Delegable("attendance.history", wrap(h.contextOfConversation))' 'tenancyadapters.Delegable("conversation.read", wrap(h.contextOfConversation))'
mut "resolving asks a key every delegate has" $AH 'tenancyadapters.Delegable("followup.resolve", wrap(h.resolveFollowUp))' 'tenancyadapters.Delegable("attendance.history", wrap(h.resolveFollowUp))'
mut "the search over earlier messages becomes delegable" $AH 'wrap(h.searchHistory))' 'tenancyadapters.Delegable("attendance.history", wrap(h.searchHistory)))'
mut "a Hub agent resolves through the member's role matrix" $AH '		resolve = h.svc.ResolveFollowUpAsHubAgent // the role matrix has nothing to say about a Hub agent: the key was proven by the route and the database' '		_ = h.svc.ResolveFollowUpAsHubAgent'
sqlmut "the sight of the memory is not asked of the database (every layer)" $M "actor_has_permission(tenant_id, current_user_id(), 'attendance.history')" "true" all
sqlmut "the resolving key is not asked of the database (every layer)" $M "actor_has_permission(tenant_id, current_user_id(), 'followup.resolve')" "true" all
sqlmut "the memory of a contact outside the contract's scope is readable (closures)" $M "AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = conversation_closures.contact_id AND ct.tenant_id = conversation_closures.tenant_id));" "AND true);"
sqlmut "the memory of a contact outside the contract's scope is readable (follow-ups)" $M "AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = follow_up_items.contact_id AND ct.tenant_id = follow_up_items.tenant_id));

-- ... resolving" "AND true);

-- ... resolving"
sqlmut "more than one instance at a time" $M "(tenant_id = acting_tenant() AND " "(true AND " all
sqlmut "a resolution may be signed by someone else" $M "AND resolved_by_user_id = current_user_id());" ");"
sqlmut "a resolved item may be rewritten" $M "         AND status = 'open'
" "" 
sqlmut "a resolution may leave the item open or reopen it" $M "AND status IN ('done', 'dropped')" "AND true"
sqlmut "with the administrator flag a reader resolves without the key" $M "CREATE POLICY follow_up_items_acting_resolve ON follow_up_items AS RESTRICTIVE FOR UPDATE
  USING ((SELECT acting_hub()) IS NULL OR actor_has_permission(tenant_id, current_user_id(), 'followup.resolve'));" "SELECT 1;"
sqlmut "with the administrator flag a delegate inserts closures" $M "CREATE POLICY conversation_closures_acting_no_insert ON conversation_closures AS RESTRICTIVE FOR INSERT
  WITH CHECK ((SELECT acting_hub()) IS NULL);" "SELECT 1;"
sqlmut "with the administrator flag a delegate inserts follow-ups" $M "CREATE POLICY follow_up_items_acting_no_insert ON follow_up_items AS RESTRICTIVE FOR INSERT
  WITH CHECK ((SELECT acting_hub()) IS NULL);" "SELECT 1;"
sqlmut "a delegate rewrites more than the resolution" $M "CREATE TRIGGER follow_up_items_acting_columns BEFORE UPDATE ON follow_up_items FOR EACH ROW EXECUTE FUNCTION follow_up_items_acting_columns();" "SELECT 1;"
sqlmut "the key has no data domain (not delegable)" $M "INSERT INTO permission_domains (permission_key, domain, need) VALUES
  ('attendance.history', 'contact', 'read'),
  ('followup.resolve',   'contact', 'read');" "SELECT 1;"
echo "PASS: all $N mutants killed"
