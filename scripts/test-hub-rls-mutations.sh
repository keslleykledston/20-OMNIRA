#!/usr/bin/env bash
# Mutation test for the Hub RLS suite: proves internal/hub/adapters/rls_integration_test.go FAILS when the
# policies are wrong. Each mutation weakens has_active_hub_access() (or adds a permissive policy) in a
# throwaway Postgres; the suite must go red every time, and green again once the original is restored.
#
# Nothing here touches omnira_dev. The throwaway database is named omnira_test_mut so the project's
# integration guard (testhelpers.RequireIntegrationDatabase) accepts it.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubmut-$$
DB=omnira_test_mut
WORK=$(mktemp -d)
cleanup() { docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=hubmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break
  sleep 1
done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)
owner_psql() { docker exec -i "$NAME" psql -U omnira -d "$DB" -X -q -v ON_ERROR_STOP=1 "$@"; }
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null

docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null

# original function body, extracted from the migration (single source of truth)
python3 - "$WORK" <<'PY'
import sys,re
s=open('migrations/000094_hub_rls_policies.up.sql').read()
m=re.search(r"CREATE OR REPLACE FUNCTION has_active_hub_access\(.*?\$\$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;", s, re.S)
pm=re.search(r"CREATE POLICY hub_inbox_read ON hub_inbox_items FOR SELECT\s+USING \((.*?)\);\nCREATE POLICY hub_inbox_insert", s, re.S)
open(sys.argv[1]+'/inbox_using.sql','w').write(pm.group(1))
open(sys.argv[1]+'/orig.sql','w').write(m.group(0)+"\n")
PY

run_suite() {
  docker run --rm --network host -v "$PWD":/app -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
    -e OMNIRA_INTEGRATION_TEST=1 \
    -e OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    -e OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    golang:1.25 go test -count=1 ./internal/hub/adapters 2>&1
}
restore() {
  owner_psql < "$WORK/orig.sql" >/dev/null
  owner_psql -c "DROP POLICY IF EXISTS mut_hub_write ON conversations" >/dev/null
  { printf 'ALTER POLICY hub_inbox_read ON hub_inbox_items USING ('; cat "$WORK/inbox_using.sql"; printf ');\n'; } | owner_psql >/dev/null
}

echo "== baseline: the suite must be GREEN on the real policies"
out=$(run_suite) || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "   green"

mutate() { # mutate <name> <python replace-from> <python replace-to>
  local name=$1 from=$2 to=$3
  python3 - "$WORK" "$from" "$to" <<'PY'
import sys
w,f,t=sys.argv[1:4]
s=open(w+'/orig.sql').read()
assert f in s, "mutation target not found: "+f
open(w+'/mut.sql','w').write(s.replace(f,t,1))
PY
  owner_psql < "$WORK/mut.sql" >/dev/null
  if out=$(run_suite); then
    echo "$out" | tail -5
    echo "FAIL: mutation '$name' SURVIVED (the suite stayed green with a broken policy)"; exit 1
  fi
  echo "   killed: $name  ($(echo "$out" | grep -c -- '--- FAIL') failing tests)"
  restore
}

echo "== mutations: each one must turn the suite RED"
mutate "ignore the tenant of the grant"            "AND g.tenant_id = p_tenant_id"                                   "AND true"
mutate "ignore the grant status (revoked works)"   "AND g.status = 'active' AND g.valid_from"                       "AND g.valid_from"
mutate "ignore the grant expiry"                   "(g.valid_until IS NULL OR g.valid_until > now())"               "true"
mutate "ignore the grant start date"               "g.valid_from <= now() AND"                                       "true AND"
mutate "ignore the contract status"                "c.status = 'active' AND"                                         "true AND"
mutate "ignore the contract expiry"                "(c.valid_until IS NULL OR c.valid_until > now())"               "true"
mutate "ignore the hub status"                     "AND h.status = 'active'"                                         "AND true"
mutate "ignore the user of the grant"              "WHERE g.user_id = p_user_id"                                    "WHERE true"
mutate "ignore the queue allowlist"                "ELSE p_queue_id IS NOT NULL AND (c.service_scope -> 'queue_ids') @> to_jsonb(p_queue_id::text)" "ELSE true"
mutate "queue scope skipped for queue-bound resources"  "WHEN NOT p_check_scope THEN true"  "WHEN true THEN true"
mutate "any session can ask about another user"    "SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())"  "SELECT (true)"
mutate "malformed scope allows instead of denying" "WHEN jsonb_typeof(c.service_scope -> 'queue_ids') <> 'array' THEN false" "WHEN jsonb_typeof(c.service_scope -> 'queue_ids') <> 'array' THEN true"

# The function is protected twice (schema-qualified relations AND a pinned search_path), so removing only one
# layer is an equivalent mutant. Drop both: the TEMP-shadowing attack must then succeed and the suite must die.
python3 - "$WORK" <<'PY2'
import sys,re
w=sys.argv[1]
s=open(w+'/orig.sql').read()
s=s.replace('public.','').replace('SET search_path = pg_catalog, public, pg_temp;','SET search_path = public;')
open(w+'/mut.sql','w').write(s)
PY2
owner_psql < "$WORK/mut.sql" >/dev/null
if out=$(run_suite); then echo "FAIL: mutation 'TEMP shadowing (unqualified relations + unpinned search_path)' SURVIVED"; exit 1; fi
echo "   killed: TEMP shadowing, both layers removed  ($(echo "$out" | grep -c -- '--- FAIL') failing tests)"
restore

echo "   mutation: a permissive write policy for Hub agents on conversations"
owner_psql -c "CREATE POLICY mut_hub_write ON conversations FOR ALL USING (has_active_hub_access(current_user_id(), tenant_id, queue_id)) WITH CHECK (has_active_hub_access(current_user_id(), tenant_id, queue_id))" >/dev/null
if out=$(run_suite); then echo "FAIL: write-policy mutation SURVIVED"; exit 1; fi
echo "   killed: hub write policy  ($(echo "$out" | grep -c -- '--- FAIL') failing tests)"
restore

policy_mutation() { # policy_mutation <name> <using expression>
  owner_psql -c "ALTER POLICY hub_inbox_read ON hub_inbox_items USING ($2)" >/dev/null
  if out=$(run_suite); then echo "FAIL: policy mutation '$1' SURVIVED"; exit 1; fi
  echo "   killed: $1  ($(echo "$out" | grep -c -- '--- FAIL') failing tests)"
  restore
}
echo "   policy mutations on hub_inbox_read"
policy_mutation "inbox trusts the projection queue (no parent conversation check)" "is_system_admin() OR has_active_hub_access(current_user_id(), tenant_id, NULL, hub_id, false)"
policy_mutation "inbox falls back to direct membership" "is_system_admin() OR has_active_membership(tenant_id, current_user_id()) OR (has_active_hub_access(current_user_id(), tenant_id, NULL, hub_id, false) AND EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = hub_inbox_items.tenant_id AND c.id = hub_inbox_items.conversation_id))"

echo "== restored: the suite must be GREEN again"
run_suite >/dev/null || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every mutation was caught by the Hub RLS suite"
