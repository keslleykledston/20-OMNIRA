#!/usr/bin/env bash
# Mutation test for the Hub WRITE path (claim + reply): weakens the Go service, the delegated sender, the HTTP handler
# and the SQL access function in several ways; the real-Postgres suite must go red every time and green on the
# original. Throwaway database only.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubreplymut-$$; DB=omnira_test_replymut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/replying/service.go internal/messages/application/delegated_send.go internal/hub/application/authorization.go internal/hub/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
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
run() {
  docker run --rm --network host -v "$PWD":/app -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false -e OMNIRA_INTEGRATION_TEST=1 \
    -e OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    -e OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    golang:1.25 go test -count=1 -run 'TestHubReply|TestDelegatedSender|TestResolveHubAccess' ./internal/hub/adapters ./internal/hub/application ./internal/messages/application 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 3 ]; }

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
mut "claim leaves no audit trail"                          $S "return s.audit(c, t, actor, \"hub.conversation.claimed\", t.Item.ConversationID, map[string]any{})" "return nil"
mut "reply leaves no audit trail"                          $S "return s.audit(c, t, actor, \"hub.message.sent\", r.Message.ID, map[string]any{\"message_id\": r.Message.ID})" "return nil"
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

# SQL layer: the same access function RLS uses, re-asked inside the writing transaction.
cp migrations/*.sql "$WORK/mig/"
python3 - "$WORK/mig/000094_hub_rls_policies.up.sql" <<'PY'
import sys
p=sys.argv[1]; s=open(p).read()
f="      AND (NOT p_require_reply OR g.can_reply)\n"
assert f in s
open(p,'w').write(s.replace(f,"",1))
PY
mkdb "$WORK/mig"
killed "SQL access function ignores can_reply" "$(run || true)"
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every Hub write-path mutation was caught"
