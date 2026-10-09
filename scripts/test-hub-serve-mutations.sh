#!/usr/bin/env bash
# Mutation test for DELEGATED SERVING (ADR-0040 phase 02, migration 108): weakens each rule in turn
# (the acting-context parser, the middleware branch, the tenant context, the audit enrichment, the SQL functions and the audit policy); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubservemut-$$; DB=omnira_test_servemut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/tenancy/adapters/serving.go internal/tenancy/adapters/http.go internal/tenancy/domain/acting.go internal/tenancy/domain/context.go internal/audit/adapters/postgres.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=servemut-$$" \
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
    go test -count=1 -p 1 -run 'TestDelegated|TestEverySecret|TestTheDoor|TestARequestActs|TestAForged|TestPermissionQuestions|TestDomainAccess|TestParseActingAs|TestActingAsString|TestHubServeContext|TestWithDelegatedContext|TestAuditNames' \
      ./internal/hub/adapters ./internal/tenancy/domain ./internal/audit/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
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
sqlmut() { # sqlmut <name> <migration file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" <<'PY'
import sys
p,f,t=sys.argv[1:4]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
SV=internal/tenancy/adapters/serving.go; HT=internal/tenancy/adapters/http.go; AC=internal/tenancy/domain/acting.go; DM=internal/tenancy/domain/context.go; AU=internal/audit/adapters/postgres.go
M=000108_hub_delegated_serving_core.up.sql
M9=000109_hub_delegated_serving_reads.up.sql   # the functions and the audit policy that 109 redefines (acting_hub()) are mutated THERE, or the override would hide the mutant

# --- Go
mut "the flag does not gate the delegated path"             $SV '	if !delegatedServing.Load() {' '	if false {'
mut "the lock's answer is ignored"                          $SV '	if locked == nil || !*locked {' '	if false {'
mut "a malformed acting header falls back to member"        $AC '	rest, ok := strings.CutPrefix(v, "hub:")
	if !ok {
		return ActingAs{}, ErrInvalidActingAs
	}' '	rest, ok := strings.CutPrefix(v, "hub:")
	if !ok {
		return ActingAs{}, nil
	}'
mut "a nil hub id is accepted as a hub context"             $AC '	if err != nil || id == uuid.Nil {' '	if err != nil {'
mut "the acting header is not read by the middleware"       $HT '			if acting.IsHub() {' '			if false {'
mut "a malformed header is not refused by the middleware"   $HT '			if aerr != nil {' '			if aerr != nil && false {'
mut "the delegated context shares the caller's slice"       $DM 'Permissions: append([]string(nil), permissions...)' 'Permissions: permissions'
mut "a delegated context may omit the grant"                $DM 'if tenantID == uuid.Nil || actorID == uuid.Nil || hubID == uuid.Nil || contractID == uuid.Nil || grantID == uuid.Nil {
		return nil, errors.New("tenant_id, actor_id, hub_id, contract_id, grant_id required for delegated serving")' 'if tenantID == uuid.Nil || actorID == uuid.Nil || hubID == uuid.Nil || contractID == uuid.Nil {
		return nil, errors.New("tenant_id, actor_id, hub_id, contract_id, grant_id required for delegated serving")'
mut "a delegated context calls itself member"               $DM '	if tc != nil && tc.Source == AccessSourceHubServe && tc.HubID != nil {' '	if false {'
mut "the audit does not name the hub of a delegated action" $AU '	set("via", "hub")' '	_ = set'
mut "the audit enrichment touches every context with a hub" $AU '	if err != nil || tc == nil || tc.Source != tenancydomain.AccessSourceHubServe || tc.HubID == nil {' '	if err != nil || tc == nil || tc.HubID == nil {'
mut "the audit enrichment overwrites the caller's keys"     $AU '		if _, ok := merged[k]; !ok {
			merged[k] = v
		}' '		merged[k] = v'

# --- SQL: delegated_permissions
sqlmut "the contract's ceiling is not applied"              $M '             WHERE k = ANY (c.delegable_permissions)
' '             WHERE true
'
sqlmut "an unmapped (never delegable) key is delegated"     $M '               AND EXISTS (SELECT 1 FROM public.permission_domains d WHERE d.permission_key = k)
' '
'
sqlmut "a suspended or expired contract still delegates"    $M "      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND g.status" "      AND g.status"
sqlmut "a revoked or expired grant still delegates"         $M "      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
    LIMIT 1" "
    LIMIT 1"
sqlmut "a suspended instance is served"                     $M "      AND t.status = 'active' AND h.status = 'active'
" "      AND h.status = 'active'
"
sqlmut "a suspended hub serves"                             $M "      AND t.status = 'active' AND h.status = 'active'
" "      AND t.status = 'active'
"
sqlmut "an inactive account is served"                      $M "      AND public.user_is_active(p_user_id)
      AND t.status" "      AND t.status"
sqlmut "anyone may ask about anyone (delegated_permissions)" $M "      AND (p_user_id = public.current_user_id() OR public.is_system_admin())
      AND public.user_is_active(p_user_id)" "      AND true
      AND public.user_is_active(p_user_id)"
sqlmut "any hub counts"                                     $M "WHERE c.tenant_id = p_tenant_id AND c.hub_id = p_hub_id
" "WHERE c.tenant_id = p_tenant_id AND (p_hub_id IS NULL OR c.hub_id = p_hub_id)
"
# --- SQL: the door
sqlmut "the acting context is never set"                    $M "  PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);
" "  NULL;
"
sqlmut "the acting context outlives the transaction"        $M "PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);" "PERFORM set_config('app.acting_hub', p_hub_id::TEXT, false);"
sqlmut "the instance is not pinned for the request"         $M "  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;" "  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;"
sqlmut "the hub is not pinned for the request"              $M "  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;" "  NULL;"
sqlmut "the contract is not pinned for the request"         $M "  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;" "  NULL;"
sqlmut "the hub membership is not pinned for the request"   $M "  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;" "  NULL;"
sqlmut "the account is not pinned for the request"          $M "  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;" "  NULL;"
sqlmut "the grant is not pinned for the request"            $M "  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;" "  NULL;"
# --- SQL: one context at a time
sqlmut "the member context is used while acting for a hub"  $M9 "           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN" "           WHEN true THEN"
sqlmut "the delegated context is used when not acting"      $M9 "           WHEN NULLIF(current_setting('app.acting_hub', true), '') IS NULL THEN" "           WHEN false THEN"
sqlmut "anyone may ask about anyone (actor_has_permission)" $M9 "SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND CASE" "SELECT true
     AND CASE"
sqlmut "write is not required for domain write"             $M9 "WHERE d.domain = p_domain AND (d.need = 'write' OR p_need = 'read'));" "WHERE d.domain = p_domain);"
sqlmut "write does not imply read inside a domain"          $M9 "WHERE d.domain = p_domain AND (d.need = 'write' OR p_need = 'read'));" "WHERE d.domain = p_domain AND d.need = p_need);"
sqlmut "classify is mapped to a read"                       $M "  ('contact.classify',   'contact',      'write')," "  ('contact.classify',   'contact',      'read'),"
# --- SQL: the audit policy
sqlmut "the audit read policy is closed (delegated insert refused)" $M9 "         AND cardinality(delegated_permissions(tenant_id, current_user_id(), public.acting_hub())) > 0);" "         AND false);"
sqlmut "a delegated agent reads every actor's audit events" $M9 "  USING (tenant_id IS NOT NULL AND actor_id = current_user_id()
         AND cardinality(delegated_permissions(tenant_id" "  USING (tenant_id IS NOT NULL
         AND cardinality(delegated_permissions(tenant_id"
sqlmut "the audit policy ignores the delegated context"     $M9 "         AND cardinality(delegated_permissions(tenant_id, current_user_id(), public.acting_hub())) > 0);" "         AND true);"
# NOT mutants (redundant layers, documented): (0) the caller guard of lock_served_tenant alone: delegated_permissions, which it calls first,
# carries the same guard and answers nothing about somebody else (the test asserts the behaviour, whichever layer provides it); (1) the hub-membership JOIN of delegated_permissions: the grant's foreign key to the
# membership already removes the grant with it (proven by the "hub membership removed" case); (2) the FIRST or the SECOND liveness check in
# lock_served_tenant alone: the other one answers (check before the locks, re-check after them); (3) `acting IS NOT NULL` in has_delegated_access
# when acting is unset: delegated_permissions then gets a NULL hub and answers nothing.
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
