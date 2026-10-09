#!/usr/bin/env bash
# Mutation test for "authorize a person by e-mail" (ADR-0039 §3.10): weakens each rule in turn (verified address, expiry, the
# author's authority, locking, validation, who may ask); the real-Postgres tests must go red every time and green on the
# original. Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubinvmut-$$; DB=omnira_test_invmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig"
FILES="internal/hub/access/invitations.go internal/platform/authn/postgres.go internal/hub/provisioning/service.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=invmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() {
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -run 'TestAccessInvite' ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  # a build error is not a kill: a real kill has at least one failing TEST
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
I=internal/hub/access/invitations.go
mut "an unverified address receives the authorization" $I 'AND i.email_verified)' 'AND true)'
mut "an expired authorization takes effect"            $I "WHERE email = \$1 AND status = 'pending' AND expires_at > now() ORDER BY created_at FOR UPDATE" "WHERE email = \$1 AND status = 'pending' ORDER BY created_at FOR UPDATE"
mut "the author's authority is not rechecked"          $I 'if !live {' 'if !live && false {'
mut "the pending rows are not locked"                  $I 'ORDER BY created_at FOR UPDATE' 'ORDER BY created_at'
mut "inviting a hub admin demotes them"                $I 'if role != provisioning.RoleAdmin {' 'if role != provisioning.RoleAdmin || true {'
mut "a company suspended meanwhile fails the rest"     $I 'if tolerant && errors.Is(err, ErrNotFound) {' 'if false && errors.Is(err, ErrNotFound) {'
mut "asking again stacks instead of replacing"         $I "WHERE hub_id = \$1 AND email = \$2 AND status = 'pending'\`, hub, e)" "WHERE hub_id = \$1 AND email = \$2 AND status = 'nonexistent'\`, hub, e)"
mut "an inactive account counts as existing"           $I "WHERE lower(email) = \$1 AND status = 'active' LIMIT 2" "WHERE lower(email) = \$1 LIMIT 2"
mut "the company is not checked at invite time"        $I 'for _, a := range access {
			if err := requireOpenCompany(c, q, hub, a.TenantID); err != nil {
				return err
			}
		}' '_ = access'
mut "the e-mail is not normalized"                     $I 'e := strings.ToLower(strings.TrimSpace(email))' 'e := strings.TrimSpace(email)'
mut "a company can be listed twice"                    $I 'case seen[a.TenantID]:' 'case false:'
mut "any mode is accepted"                             $I 'case a.Mode != "read" && a.Mode != "reply":' 'case false:'
mut "the number of companies is unbounded"             $I 'if len(access) > maxInviteAccess {' 'if false {'
mut "cancelling twice succeeds"                        $I "WHERE id = \$1 AND hub_id = \$2 AND status = 'pending'\`, id, hub)" "WHERE id = \$1 AND hub_id = \$2\`, id, hub)"
mut "another hub can cancel it"                        $I "WHERE id = \$1 AND hub_id = \$2 AND status = 'pending'\`, id, hub)" "WHERE id = \$1 AND status = 'pending'\`, id, hub)"
mut "expired authorizations are still listed"          $I "WHERE hub_id = \$1 AND status = 'pending' AND expires_at > now() ORDER BY created_at DESC, id" "WHERE hub_id = \$1 AND status = 'pending' ORDER BY created_at DESC, id"
mut "an applied authorization is not marked applied"   $I "SET status = 'applied', applied_user_id = \$2, applied_at = now(), updated_at = now() WHERE id = \$1\`, p.id, user)" "SET updated_at = now() WHERE id = \$1 AND \$2::uuid IS NOT NULL\`, p.id, user)"
# NOT a mutant: dropping the HANDLER's admin check alone survives by design, because the service asks the database again inside its
# own transaction (access.Service.tx): "who may ask" has two independent layers and the tests prove the pair (hub agent, company
# admin and anonymous are refused, nothing is written). Removing both layers at once is not mutated here.
mut "the author's membership is not pinned while applying" $I '{`SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2 FOR SHARE`, []any{hub, person}},' '{`SELECT 1 FROM hub_memberships WHERE hub_id = $1 AND user_id = $2`, []any{hub, person}},'
mut "the company is not pinned while applying"         $I 'if _, err := platformdb.LockTenantActive(ctx, q, tenant); err != nil {
		return err
	}' '_ = platformdb.LockTenantActive'
mut "the application opens its own connections again" internal/hub/provisioning/service.go 'if q, ok := ctx.Value(joinKey{}).(platformdb.Querier); ok {' 'if q, ok := ctx.Value(joinKey{}).(platformdb.Querier); ok && false {'
mut "the sign-in hook never runs"                      internal/platform/authn/postgres.go 'if err == nil && r.afterProvision != nil {' 'if false && err == nil && r.afterProvision != nil {'
# SQL layer: the trail cannot be erased by the application role (migration 102)
rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
python3 - "$WORK/mig/000102_hub_preauthorizations.up.sql" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
a="REVOKE DELETE, TRUNCATE ON hub_preauthorizations FROM omnira_app;"
assert a in s, "SQL mutation target not found"
open(p,'w').write(s.replace(a,"",1))
PY
mkdb "$WORK/mig"
killed "the application role can delete the trail" "$(run || true)"
mkdb "$PWD/migrations"
echo "PASS: every mutant was killed"
