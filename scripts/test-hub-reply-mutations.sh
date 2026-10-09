#!/usr/bin/env bash
# Mutation test for the Hub WRITE path (claim + reply), the suspended-company rule (098) and platform operators (099): weakens the Go service, the delegated sender, the HTTP handler
# and the SQL access function in several ways; the real-Postgres suite must go red every time and green on the
# original. Throwaway database only.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubreplymut-$$; DB=omnira_test_replymut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/provisioning/operators.go internal/hub/replying/service.go internal/messages/application/delegated_send.go internal/hub/application/authorization.go internal/hub/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=replymut-$$" \
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
    go test -count=1 -run 'TestHubReply|TestHub_Suspended|TestHubAccess|TestPlatformOperator|TestDelegatedSender|TestResolveHubAccess' ./internal/hub/adapters ./internal/hub/application ./internal/hub/provisioning ./internal/messages/application 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 4 ]; }

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
S=internal/hub/replying/service.go; D=internal/messages/application/delegated_send.go; A=internal/hub/application/authorization.go; H=internal/hub/adapters/http.go
mut "authorization does not require the reply capability"  $S "RequireReply: true," "RequireReply: false,"
mut "company on screen is not compared with the item's"    $S "if expectedTenant != item.TenantID {" "if false {"
mut "delegation is not re-checked inside the transaction"  $S "	if !ok {
		return nil, false, application.ErrAccessDenied
	}
	return assigned, status == \"closed\", nil" "	_ = ok
	return assigned, status == \"closed\", nil"
mut "a conversation held by another agent can be taken"    $S "			return ErrTaken" "			return nil"
mut "finalized conversations can be claimed"               $S "		if closed {
			return ErrClosed
		}" "		_ = closed"
mut "claim leaves no audit trail"                          $S "return s.audit(c, t, actor, \"hub.conversation.claimed\", \"conversation\", t.Item.ConversationID, map[string]any{})" "return nil"
mut "reply leaves no audit trail"                          $S "return s.audit(c, t, actor, \"hub.message.sent\", \"message\", r.Message.ID, map[string]any{\"message_id\": r.Message.ID})" "return nil"
mut "sender does not need to be the assignee"              $D "case *sc.AssignedTo != actor:" "case false:"
mut "unassigned conversation can be answered"              $D "case sc.AssignedTo == nil:" "case false:"
mut "guard is skipped"                                     $D "if guard != nil {" "if false {"
mut "any context type may use the delegated sender"        $D "tc.Source != tenancydomain.AccessSourceSystem ||" "false ||"
mut "assignee not required atomically at insert"           $D "d.store.InsertQueued(ctx, actor, *sc, text, idempotencyKey, hash, true)" "d.store.InsertQueued(ctx, actor, *sc, text, idempotencyKey, hash, false)"
mut "grant capability ignored by the authorization service" $A "if req.RequireReply && !grant.CanReply {" "if false {"
mut "unknown body fields accepted"                         $H "dec.DisallowUnknownFields()" "_ = dec"
mut "no-reply grant reported as not found"                 $H "case errors.Is(err, application.ErrReplyNotAllowed):
		httpError(w, \"your access to this company is read-only\", http.StatusForbidden)" "case false:
		httpError(w, \"your access to this company is read-only\", http.StatusForbidden)"

O=internal/hub/provisioning/operators.go
mut "a disabled user can become an operator"               $O "		if err := requireActiveUser(c, q, user); err != nil {
			return err
		}
		var prev *string" "		var prev *string"
mut "revoking leaves the operator active"                  $O "UPDATE platform_operators SET status = 'revoked', revoked_at = now(), updated_at = now() WHERE user_id = \$1" "UPDATE platform_operators SET updated_at = now() WHERE user_id = \$1"
mut "granting an operator leaves no audit trail"           $O "return s.audit(c, q, nil, \"platform.operator.added\", \"platform_operator\", user, map[string]any{\"user_id\": user, \"reactivated\": prev != nil})" "return nil"

# SQL layer: the same access function RLS uses (redefined by 098 on top of 094), re-asked inside the writing transaction,
# plus the platform operator table, policies and function (099).
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
sqlmut "SQL access function ignores can_reply"            000098_hub_access_requires_active_tenant.up.sql "      AND (NOT p_require_reply OR g.can_reply)
" ""
sqlmut "SQL access function ignores the company's status" 000098_hub_access_requires_active_tenant.up.sql "      AND t.status = 'active'
" ""
sqlmut "is_platform_operator answers for any user"        000099_platform_operators.up.sql "SELECT (p_user_id = public.current_user_id() OR public.is_system_admin())
     AND EXISTS (SELECT 1 FROM public.platform_operators" "SELECT true
     AND EXISTS (SELECT 1 FROM public.platform_operators"
sqlmut "anyone can read every operator row"               000099_platform_operators.up.sql "USING (is_system_admin() OR user_id = current_user_id());
CREATE POLICY platform_operators_insert" "USING (true);
CREATE POLICY platform_operators_insert"
sqlmut "anyone can insert operator rows (self-promotion)" 000099_platform_operators.up.sql "FOR INSERT WITH CHECK (is_system_admin());" "FOR INSERT WITH CHECK (true);"
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every Hub write-path, suspension and platform-operator mutation was caught"
