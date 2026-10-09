#!/usr/bin/env bash
# Mutation test for Hub-delegated MANAGEMENT of channels/integrations (ADR-0038 phase 3, migration 103): weakens each rule in turn
# (middleware, permission checker, service guards, the SQL function and the row-level policies); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubmanagemut-$$; DB=omnira_test_managemut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/adapters/manage_http.go internal/channels/adapters/management.go internal/channels/application/connection_management.go internal/channels/application/erp_connections.go internal/channels/application/waha_connections.go internal/tenancy/domain/context.go internal/hub/provisioning/service.go internal/hub/companies/service.go internal/hub/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=managemut-$$" \
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
    go test -count=1 -run 'TestHubManage|TestOperatorDelegates|TestManageSwitch|TestManagedInstances' ./internal/hub/adapters 2>&1
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
MW=internal/hub/adapters/manage_http.go; CK=internal/channels/adapters/management.go; MS=internal/channels/application/connection_management.go
ES=internal/channels/application/erp_connections.go; WS=internal/channels/application/waha_connections.go; DM=internal/tenancy/domain/context.go
PV=internal/hub/provisioning/service.go; CO=internal/hub/companies/service.go; HT=internal/hub/adapters/http.go

# --- Go: middleware
# NOT a mutant: dropping the lock's own "denied" answer alone survives by design, because the contract query right after it asks
# has_hub_manage_access again (two independent layers: lock+eligibility, then hub-matched contract). The tests prove the pair; the
# lock itself is proven by TestHubManagerHoldsTheCompanyActiveForTheRequest (mutants below).
mut "the contract/hub of the path is not matched"          $MW 'WHERE c.hub_id = $1 AND c.tenant_id = $2 AND has_hub_manage_access($2, $3, NULL, $1)' 'WHERE c.tenant_id = $2 AND has_hub_manage_access($2, $3, NULL, NULL)'
mut "the company is not held active for the request"       $MW 'SELECT lock_managed_tenant($1, $2, $3)' 'SELECT true'
# --- Go: permission checker
mut "hub managers get every tenant permission"             $CK '		default:
			return false, nil
		}
		var ok bool' '		default:
			scope = "channels"
		}
		var ok bool'
mut "the scope is not asked of the database"              $CK 'SELECT has_hub_manage_access($1, $2, $3, $4)' 'SELECT has_hub_manage_access($1, $2, NULL, $4)'
mut "another actor can be asked about"                     $CK 'if userID != tc.ActorID || tc.HubID == nil {' 'if tc.HubID == nil {'
mut "integration permission follows the channel scope"     $CK '			scope = "integrations"' '			scope = "channels"'
# --- Go: services
mut "any access source may manage (domain guard)"         $DM '	switch tc.Source {
	case AccessSourceDirect:
		return true' '	switch tc.Source {
	case AccessSourceDirect, AccessSourceHub:
		return true'
mut "hub management without a contract is accepted"        $DM 'return tc.HubID != nil && *tc.HubID != uuid.Nil && tc.ServiceContractID != nil && *tc.ServiceContractID != uuid.Nil' 'return true'
mut "the catalog needs only one permission, not either"    $MS '		if ok {
			return nil
		}
	}
	return ErrConnForbidden' '		if !ok {
			return nil
		}
	}
	return ErrConnForbidden'
mut "ERP uses the channel permission"                      $ES 'PermissionIntegrationManage)' 'PermissionChannelManage)'
mut "listing shows what is forbidden"                      $MS 'if errors.Is(err, ErrConnForbidden) {
			continue // delegated scopes differ per provider: what this person may not manage is simply not listed
		}' 'if false {
		}'
# --- Go: switch and scopes
mut "renewing a grant revives management"                  $PV "can_manage = CASE WHEN effective_access_grants.status = 'active' THEN effective_access_grants.can_manage ELSE false END," "can_manage = effective_access_grants.can_manage,"
mut "revoking keeps the management switch"                 $PV "SET status = 'revoked', can_manage = false," "SET status = 'revoked',"
mut "management without an active grant is accepted"       $PV 'if status != "active" {
			return invalid("the grant is %s: give the person access first", status)
		}' 'if false {
		}'
mut "any scope name is accepted"                           $CO 'if !slices.Contains(ManageableScopes, sc) {' 'if false && !slices.Contains(ManageableScopes, sc) {'
mut "the scope change is not audited"                      $CO '"platform.company.management_scopes_changed"' '"platform.company.status_changed"'
mut "the hub flag ignores the database"                    $HT 'WHERE c.hub_id = $1 AND has_hub_manage_access(c.tenant_id, $2, NULL, $1))' 'WHERE c.hub_id = $1 AND true)'
# --- SQL: the function and the policies (migration 103)
M=000103_hub_management_delegation.up.sql
sqlmut "the scope is not required"                         $M "AND CASE WHEN p_scope IS NULL THEN cardinality(c.management_scopes) > 0 ELSE p_scope = ANY (c.management_scopes) END" "AND true"
sqlmut "an agent without can_manage manages"               $M "WHERE g.service_contract_id = c.id AND g.hub_id = c.hub_id AND g.user_id = p_user_id AND g.can_manage" "WHERE g.service_contract_id = c.id AND g.hub_id = c.hub_id AND g.user_id = p_user_id"
sqlmut "a revoked grant still manages"                     $M "AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
           )" "AND true
           )"
sqlmut "a suspended company is managed"                    $M "AND t.status = 'active' AND h.status = 'active'
         AND c.status = 'active'" "AND h.status = 'active'
         AND c.status = 'active'"
sqlmut "a suspended contract still delegates"              $M "AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
         AND CASE" "AND CASE"
sqlmut "anyone may ask about anyone"                       $M "SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND public.user_is_active(p_user_id)
     AND EXISTS (
       SELECT 1
       FROM public.hub_tenant_service_contracts c" "SELECT true
     AND public.user_is_active(p_user_id)
     AND EXISTS (
       SELECT 1
       FROM public.hub_tenant_service_contracts c"
sqlmut "another hub's contract counts"                     $M "AND (p_hub_id IS NULL OR c.hub_id = p_hub_id)" "AND true"
sqlmut "the ERP rows fall under the channels scope"        $M "SELECT CASE WHEN p_channel = 'erp' THEN 'integrations' ELSE 'channels' END;" "SELECT 'channels'::TEXT;"
sqlmut "the connection insert policy is open"              $M "CREATE POLICY channel_connections_hub_manage_insert ON channel_connections FOR INSERT
  WITH CHECK (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));" "CREATE POLICY channel_connections_hub_manage_insert ON channel_connections FOR INSERT
  WITH CHECK (true);"
sqlmut "the connection read policy is open"                $M "CREATE POLICY channel_connections_hub_manage_read ON channel_connections FOR SELECT
  USING (has_hub_manage_access(tenant_id, current_user_id(), channel_connection_scope(channel)));" "CREATE POLICY channel_connections_hub_manage_read ON channel_connections FOR SELECT
  USING (true);"
# NOT a mutant: the UPDATE policy of the manage path. An UPDATE that reads columns must ALSO satisfy the SELECT policies, for the existing
# row (USING) and for the NEW row (PostgreSQL applies them as a check on the result), so a row can neither be touched nor moved outside
# the scope by relaxing only the update policy: the manage-read policy mutated above is the layer that holds. The tests still assert the
# behaviour (no move to another instance or scope), whatever layer provides it.
sqlmut "the credential insert policy is open"              $M "WITH CHECK (EXISTS (SELECT 1 FROM channel_connections c WHERE c.id = connection_id AND c.tenant_id = channel_credentials.tenant_id
    AND has_hub_manage_access(c.tenant_id, current_user_id(), channel_connection_scope(c.channel))));
CREATE POLICY channel_credentials_hub_manage_update" "WITH CHECK (true);
CREATE POLICY channel_credentials_hub_manage_update"
sqlmut "the entitlement row is hidden from the manager"    $M "USING (has_hub_manage_access(tenant_id, current_user_id()));" "USING (false);"
sqlmut "the audit insert is refused for the manager"       $M "USING (tenant_id IS NOT NULL AND actor_id = current_user_id() AND has_hub_manage_access(tenant_id, current_user_id()));" "USING (false);"
sqlmut "the company is not pinned for the request"         $M "  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;" "  NULL;"
sqlmut "the hub is not pinned for the request"             $M "  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;" "  NULL;"
sqlmut "the contract is not pinned for the request"        $M "  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;" "  NULL;"
sqlmut "the membership is not pinned for the request"      $M "  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;" "  NULL;"
sqlmut "the account is not pinned for the request"         $M "  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;" "  NULL;"
sqlmut "the grant is not pinned for the request"           $M "  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;" "  NULL;"
sqlmut "an inactive account manages"                       $M "     AND public.user_is_active(p_user_id)
     AND EXISTS (
       SELECT 1
       FROM public.hub_tenant_service_contracts c" "     AND EXISTS (
       SELECT 1
       FROM public.hub_tenant_service_contracts c"
sqlmut "a hub admin is not eligible without a grant"       $M "EXISTS (SELECT 1 FROM public.roles r WHERE r.id = hm.role_id AND r.tenant_id IS NULL AND r.key = 'hub_admin')" "false"
# NOT a mutant: dropping the caller guard of managed_instances() alone survives by design, because the function it calls
# (has_hub_manage_access) carries the same guard: asked about somebody else it answers false for every instance, so the list is empty.
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
