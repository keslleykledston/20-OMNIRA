#!/usr/bin/env bash
# Mutation test for the control plane (ADR-0038 phase 1): weakens the companies service, the capability gate, the channel
# gate and the SQL policies of migration 100; the real-Postgres suite must go red every time and green on the original.
# Throwaway database only.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubadminmut-$$; DB=omnira_test_adminmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/companies/service.go internal/entitlements/entitlements.go internal/hub/adapters/admin_http.go internal/channels/application/connection_management.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=adminmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm module and build caches (a cold container recompiles everything per mutant)
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -run 'TestHubAdmin|TestHub_Suspended|TestConnectionManagementHonours' ./internal/hub/adapters ./internal/channels/application 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 2 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -20; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() { # killed <name> <output>
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  echo "$2" | grep -q "^FAIL" || { echo "$2" | tail -5; echo "FAIL: mutation '$1' was inconclusive"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
mut() { # mut <name> <file> <from> <to>
  python3 - "$2" "$WORK/orig/$2" "$3" "$4" <<'PY'
import sys
p,orig,f,t=sys.argv[1:5]
s=open(orig).read()
assert f in s, "mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
C=internal/hub/companies/service.go; E=internal/entitlements/entitlements.go; H=internal/hub/adapters/admin_http.go; G=internal/channels/application/connection_management.go
mut "service does not require a platform operator"        $C "SELECT is_platform_operator(\$1) AND is_hub_admin(\$2, \$1)" "SELECT is_hub_admin(\$2, \$1)"
mut "service does not require a hub administrator"        $C "SELECT is_platform_operator(\$1) AND is_hub_admin(\$2, \$1)" "SELECT is_platform_operator(\$1)"
mut "service ignores a suspended hub"                     $C "       AND EXISTS (SELECT 1 FROM service_hubs WHERE id = \$2 AND status = 'active')" "       AND true"
mut "idempotency key reuse with another body is accepted" $C "			if prevHash != hash {
				return ErrKeyMismatch
			}" "			_ = prevHash"
mut "a created company gets no contract with the hub"     $C "if _, err := q.Exec(c, \`INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id) VALUES (\$1, \$2)\`, hub, tenant); err != nil {
			return err
		}" "_ = tenant"
mut "a created company gets no default queue"             $C "if _, err := q.Exec(c, \`INSERT INTO queues (tenant_id, name, mode, is_default) VALUES (\$1, 'Default', 'manual', true)\`, tenant); err != nil {
			return err
		}" "_ = tenant"
mut "creation leaves no audit trail"                      $C "if err := audit(c, q, tenant, operator, \"platform.company.created\", meta); err != nil {
			return err
		}" "_ = meta"
mut "an inactive company can be switched from here"       $C "if from != \"active\" && from != \"suspended\" {" "if false {"
mut "unknown capabilities are accepted"                   $C "if !entitlements.Known(k) {" "if false {"
mut "every capability write is audited, even a no-op"     $C "if effective != want {" "if true {"
mut "status change leaves no audit trail"                 $C "if err := audit(c, q, tenant, operator, \"platform.company.status_changed\", map[string]any{\"hub_id\": hub, \"from\": from, \"to\": in.Status}); err != nil {
				return err
			}" "_ = from"
mut "the gate always says enabled"                        $E "	return enabled, err
}" "	return true, err
}"
mut "unknown capability keys count as enabled"            $E "	if !Known(capability) {
		return false, nil
	}" ""
mut "the middleware never refuses"                        $E "if err := c.Gate(r.Context(), tc.TenantID, capability); err != nil {" "if err := c.Gate(r.Context(), tc.TenantID, capability); false && err != nil {"
mut "creating a company does not need an Idempotency-Key"  $H "	if key == \"\" {" "	if false {"
mut "channel gate is skipped"                             $G "	if s.gate == nil {
		return nil
	}" "	if true {
		return nil
	}"
mut "ERP/CRM is gated by the WhatsApp switch"             $G "	if channel == domain.ChannelERP {" "	if false {"

sqlmut() { # sqlmut <name> <migration file> <from> <to>
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
M=000100_tenant_entitlements.up.sql
sqlmut "any member reads every company's switches"        $M "USING (is_system_admin() OR has_active_membership(tenant_id, current_user_id()));" "USING (true);"
sqlmut "a company member can insert its own switches"     $M "CREATE POLICY tenant_entitlements_insert ON tenant_entitlements FOR INSERT WITH CHECK (is_system_admin());" "CREATE POLICY tenant_entitlements_insert ON tenant_entitlements FOR INSERT WITH CHECK (true);"
sqlmut "a company member can change its own switches"     $M "CREATE POLICY tenant_entitlements_update ON tenant_entitlements FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());" "CREATE POLICY tenant_entitlements_update ON tenant_entitlements FOR UPDATE USING (true) WITH CHECK (true);"
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every control-plane mutation was caught"
