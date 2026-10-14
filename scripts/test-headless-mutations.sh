#!/usr/bin/env bash
# Mutation test for the Headless / external-application invariants (ADR-0046): weakens the Go code and the SQL of the credential, the
# authentication, the in-transaction recheck and the row policies in many ways; the real-Postgres suite must go red every time and green
# on the original. Throwaway database only. Sections are added phase by phase (H1 credentials/authentication; H2 control; H4 journal; H5 writes).
#   usage: scripts/test-headless-mutations.sh [phase ...]   (default: every phase that exists)
set -euo pipefail
cd "$(dirname "$0")/.."
PHASES=${*:-"h1"}
NAME=omnira-headlessmut-$$; DB=omnira_test_headlessmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/external/domain/token.go internal/external/application/auth.go internal/external/adapters/middleware.go internal/external/adapters/recheck.go internal/external/adapters/admin_http.go internal/external/adapters/store.go internal/external/adapters/problem.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=headlessmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm module and build caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -timeout 120s ./internal/external/adapters ./internal/external/application 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 2 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -20; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() { # killed <name> <output>
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  echo "$2" | grep -q "^FAIL" || { echo "$2" | tail -5; echo "FAIL: mutation '$1' was inconclusive"; exit 1; }
  if [ "$(echo "$2" | grep -c -- '--- FAIL')" -eq 0 ]; then echo "$2" | tail -8; echo "FAIL: mutation '$1' only broke the build (not a real kill)"; exit 1; fi
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0; FROM=${MUT_FROM:-1}   # MUT_FROM=<n> resumes at the n-th mutant (a long run that stopped on a survivor need not repeat the ones already killed)
skip() { N=$((N+1)); [ "$N" -lt "$FROM" ]; }
mut() { # mut <name> <file> <from> <to>
  skip && return 0
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
sqlmut() { # sqlmut <name> <migration file> <from> <to>
  skip && return 0
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

for phase in $PHASES; do
case "$phase" in
h1)
  echo "== H1: credentials, authentication, recheck, policies"
  T=internal/external/domain/token.go; A=internal/external/application/auth.go; M=internal/external/adapters/middleware.go
  R=internal/external/adapters/recheck.go; H=internal/external/adapters/admin_http.go; S=internal/external/adapters/store.go
  mut "any secret matches the stored digest"                $T "return subtle.ConstantTimeCompare(d, storedDigest) == 1" "return subtle.ConstantTimeCompare(d, storedDigest) == 1 || true"
  mut "the secret is never compared"                        $A "if !domain.SecretMatches(secret, rec.Digest) {" "if false {"
  mut "a stored scope this build does not know is accepted" $A "	scopes, err := domain.ParseScopes(rec.Scopes)
	if err != nil {" "	scopes, _ := domain.ParseScopes(rec.Scopes)
	if false {"
  mut "the tenant can be chosen by query"                   $M "r.URL.Query().Has(\"tenant_id\") ||" "false ||"
  mut "the tenant can be chosen by header"                  $M "|| r.Header.Get(\"X-Tenant-ID\") != \"\"" "|| false"
  mut "acting-as header is tolerated"                       $M "|| r.Header.Get(\"X-Omnira-Acting-As\") != \"\"" "|| false"
  mut "no per-installation rate limit"                      $M "if allowed, wait := m.perApp.Allow(\"app:\"+p.InstallationID.String(), p.RatePerMin); !allowed {" "if allowed, wait := m.perApp.Allow(\"app:\"+p.InstallationID.String(), p.RatePerMin); !allowed && false {"
  mut "failed authentication is never throttled"            $M "if m.FailedAuthPerMinute > 0 {" "if false {"
  mut "scope is not checked"                                $M "if !p.Has(scope) {" "if !p.Has(scope) && false {"
  mut "the throttle keys on the proxy, not on the client"   $M "(ip.IsLoopback() || ip.IsPrivate()) {" "(false) {"
  mut "a public peer may choose its own throttle key"       $M "ip != nil && (ip.IsLoopback() || ip.IsPrivate()) {" "ip != nil {"
  mut "audit rows ignore the request id"                    $H "correlation = u // the log line and the audit row of one request share the id" "_ = u"
  mut "the recheck always passes"                           $R "if !ok {" "if false {"
  mut "unknown JSON fields are accepted by the admin API"   $H "dec.DisallowUnknownFields()" "_ = dec"
  mut "a revoked installation can still be edited"          $S "	if cur.Status == domain.StatusRevoked {
		return cur, domain.ErrInstallationRevoked
	}
	if in.Name != nil {" "	if in.Name != nil {"
  mut "an installation can be revoked twice"                $S "case t == Revoke && cur.Status != domain.StatusRevoked:" "case t == Revoke:"
  mut "more than two live credentials are allowed"          $S "if live >= domain.MaxLiveCredentials {" "if false {"
  mut "the credential is not revoked with the installation" $S "UPDATE external_app_credentials SET revoked_at = now() WHERE tenant_id = \$1 AND installation_id = \$2 AND revoked_at IS NULL\`, tenantID, id); err == nil {" "SELECT 1\`); err == nil {"
  sqlmut "lookup ignores the installation status"           000135_external_app_installations.up.sql "     AND i.status = 'active'
     AND t.status = 'active'
\$\$;" "     AND t.status = 'active'
\$\$;"
  sqlmut "lookup ignores the company status"                000135_external_app_installations.up.sql "     AND t.status = 'active'
\$\$;" "\$\$;"
  sqlmut "lookup ignores credential expiry"                 000135_external_app_installations.up.sql "     AND c.revoked_at IS NULL AND c.expires_at > now()
     AND i.status = 'active'" "     AND c.revoked_at IS NULL
     AND i.status = 'active'"
  sqlmut "recheck does not lock (revocation does not wait)" 000135_external_app_installations.up.sql "      FOR SHARE OF i, c, t;" "      ;"
  sqlmut "recheck ignores a revoked credential"             000135_external_app_installations.up.sql "      AND c.id = p_credential AND c.revoked_at IS NULL AND c.expires_at > clock_timestamp()" "      AND c.id = p_credential"
  sqlmut "recheck is not bound to the tenant"               000135_external_app_installations.up.sql "    WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
      AND c.id = p_credential" "    WHERE i.id = p_installation AND i.status = 'active'
      AND c.id = p_credential"
  sqlmut "touch is not bound to the installation"           000135_external_app_installations.up.sql "   WHERE tenant_id = p_tenant AND installation_id = p_installation AND id = p_credential" "   WHERE id = p_credential"
  sqlmut "allow-list door is not bound to the tenant"       000135_external_app_installations.up.sql "   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
\$\$;" "   WHERE i.id = p_installation AND i.status = 'active'
\$\$;"
  sqlmut "the application role can read the digest"         000135_external_app_installations.up.sql "FROM PUBLIC, omnira_app;" "FROM PUBLIC;"
  sqlmut "a system session can read installations"          000135_external_app_installations.up.sql "FOR SELECT USING (actor_has_permission(tenant_id, current_user_id(), 'integration.read'))\$p\$, t || '_read', t);" "FOR SELECT USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'integration.read'))\$p\$, t || '_read', t);"
  sqlmut "recheck does not lock the company (suspension does not wait)" 000135_external_app_installations.up.sql "      FOR SHARE OF i, c, t;" "      FOR SHARE OF i, c;"
  sqlmut "recheck judges expiry with the start of the transaction"      000135_external_app_installations.up.sql "c.expires_at > clock_timestamp()" "c.expires_at > now()"
  sqlmut "allow-list door ignores the company status"                   000135_external_app_installations.up.sql "    JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'" "   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'"
  sqlmut "a queue of another company is accepted in the allow-list" 000135_external_app_installations.up.sql "  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE" "  FOREIGN KEY (queue_id) REFERENCES queues (id) ON DELETE CASCADE"
  ;;
*) echo "unknown phase $phase"; exit 2;;
esac
done
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every Headless mutation was caught ($PHASES)"
