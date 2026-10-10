#!/usr/bin/env bash
# Proves the Hub migrations (093..112) against a throwaway Postgres, using the PRODUCTION migrator
# (tools/migrate-sql.sh: one transaction per migration, ON_ERROR_STOP, schema_migrations ledger).
#
#   1. apply everything up to 092 only            -> dump A (pre-Hub schema)
#   2. apply 093..112                             -> dump B
#   3. roll back 095, 094, 093 (reverse order)    -> dump C   must equal A  (down really undoes up)
#   4. apply 093..112 again                       -> dump D   must equal B  (up is reproducible)
#   5. catalogue checks: every tenant_id table has RLS+FORCE+policy; omnira_app is not superuser/bypassrls
#
# Nothing here touches omnira_dev. Container is labeled like scripts/test-integration.sh.
set -euo pipefail
cd "$(dirname "$0")/.."
FIRST_HUB=000093
NAME=omnira-hubmig-$$
WORK=$(mktemp -d)
cleanup() { docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

mkdir -p "$WORK/pre"
for f in migrations/*.sql; do
  v=$(basename "$f" | cut -c1-6)
  if [ "$v" \< "${FIRST_HUB:0:6}" ]; then cp "$f" "$WORK/pre/"; fi
done

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=hubmig-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=hubmig postgres:16-alpine >/dev/null
for i in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break
  sleep 1
done
docker exec "$NAME" pg_isready -U omnira -d hubmig >/dev/null || { echo "FAIL: throwaway postgres did not start"; exit 1; }

run() { # run <migrations dir> <migrate-sql args...>
  local dir=$1; shift
  docker run --rm --network "container:$NAME" -v "$dir:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=hubmig postgres:16-alpine sh /tools/migrate-sql.sh "$@"
}
dump() {
  docker exec "$NAME" pg_dump -U omnira -d hubmig --schema-only --no-owner \
    | grep -v '^--' | grep -v '^$' | grep -v 'schema_migrations' | grep -v '^.restrict ' | grep -v '^.unrestrict '
}
psql_q() { docker exec "$NAME" psql -U omnira -d hubmig -X -At -c "$1"; }

echo "== 1. baseline (<= 092)"
run "$WORK/pre" up | tail -1
dump > "$WORK/A.sql"

echo "== 2. up 093..112"
run "$PWD/migrations" up | grep -E "applying|up to date"
dump > "$WORK/B.sql"
[ "$(psql_q "SELECT max(version) FROM schema_migrations")" = "$(ls migrations/*.up.sql | sort | tail -1 | xargs basename | sed 's/.up.sql//')" ] \
  || { echo "FAIL: ledger is not at the newest migration"; exit 1; }

echo "== 3. down 095, 094, 093"
for v in $(ls migrations/*.up.sql | sort -r | xargs -n1 basename | sed 's/.up.sql//' | awk -v f="$FIRST_HUB" '$0 >= f'); do
  run "$PWD/migrations" down "$v" | tail -1
done
dump > "$WORK/C.sql"
if ! diff -q "$WORK/A.sql" "$WORK/C.sql" >/dev/null; then
  echo "FAIL: schema after rolling back the Hub migrations differs from the pre-Hub schema"; diff "$WORK/A.sql" "$WORK/C.sql" | head -30; exit 1
fi
echo "   schema after down == pre-Hub schema"

echo "== 4. up again"
run "$PWD/migrations" up | grep -E "up to date"
dump > "$WORK/D.sql"
if ! diff -q "$WORK/B.sql" "$WORK/D.sql" >/dev/null; then
  echo "FAIL: schema after up/down/up differs from the first up"; diff "$WORK/B.sql" "$WORK/D.sql" | head -30; exit 1
fi
echo "   schema after up/down/up == first up"
removed=$(diff "$WORK/A.sql" "$WORK/B.sql" | grep -c '^>' || true)
[ "$removed" -gt 100 ] || { echo "FAIL: Hub migrations added suspiciously little ($removed lines)"; exit 1; }
echo "   Hub migrations add $removed schema lines"

echo "== 5. catalogue checks"
bad=$(psql_q "SELECT string_agg(c.relname, ',') FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
  WHERE n.nspname='public' AND c.relkind='r'
    AND EXISTS (SELECT 1 FROM information_schema.columns col WHERE col.table_schema='public' AND col.table_name=c.relname AND col.column_name='tenant_id')
    AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity
         OR NOT EXISTS (SELECT 1 FROM pg_policies p WHERE p.tablename=c.relname))")
[ -z "$bad" ] || { echo "FAIL: tenant_id tables without RLS+FORCE+policy: $bad"; exit 1; }
hubbad=$(psql_q "SELECT string_agg(c.relname, ',') FROM pg_class c WHERE c.relnamespace='public'::regnamespace AND c.relkind='r'
  AND c.relname IN ('service_hubs','hub_memberships','hub_tenant_service_contracts','work_pools','work_pool_members','skills','agent_skills','effective_access_grants','hub_inbox_items','platform_operators','tenant_entitlements','company_creation_requests','hub_preauthorizations')
  AND (NOT c.relrowsecurity OR NOT c.relforcerowsecurity)")
[ -z "$hubbad" ] || { echo "FAIL: Hub tables without RLS+FORCE: $hubbad"; exit 1; }
unpinned=$(psql_q "SELECT string_agg(p.proname, ',') FROM pg_proc p JOIN pg_namespace n ON n.oid=p.pronamespace
  WHERE n.nspname='public' AND p.prosecdef AND coalesce(array_to_string(p.proconfig, ','),'') NOT LIKE '%search_path=pg_catalog, public, pg_temp%'")
[ -z "$unpinned" ] || { echo "FAIL: SECURITY DEFINER functions without search_path pg_catalog, public, pg_temp: $unpinned"; exit 1; }
role=$(psql_q "SELECT rolsuper OR rolbypassrls FROM pg_roles WHERE rolname='omnira_app'")
[ "$role" = "f" ] || { echo "FAIL: omnira_app can bypass RLS"; exit 1; }
echo "   every tenant_id table has RLS+FORCE+policy; the 13 Hub and control-plane tables have RLS+FORCE; every SECURITY DEFINER function pins pg_temp last; omnira_app cannot bypass RLS"
echo "PASS: Hub migrations 093..112 apply, roll back to the exact pre-Hub schema, and re-apply identically"
