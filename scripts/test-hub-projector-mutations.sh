#!/usr/bin/env bash
# Mutation test for the Hub inbox projector: weakens the reconcile SQL in nine ways; the real-Postgres suite
# (internal/worker/hubprojector) must go red every time and green on the original. Throwaway database only.
set -euo pipefail
cd "$(dirname "$0")/.."
F=internal/worker/hubprojector/projector.go
NAME=omnira-hubprojmut-$$; DB=omnira_test_projmut
WORK=$(mktemp -d); cp "$F" "$WORK/orig.go"
cleanup() { cp "$WORK/orig.go" "$F"; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=projmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null
docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null

run() {
  docker run --rm --network host -v "$PWD":/app -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false -e OMNIRA_INTEGRATION_TEST=1 \
    -e OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    -e OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    golang:1.25 go test -count=1 ./internal/worker/hubprojector 2>&1
}
# NOTE: exit status of a pipeline ending in grep is grep's, so the verdict is read from the output.
out=$(run || true); echo "$out" | grep -q "^ok" || { echo "$out" | tail -20; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

mut() { # mut <name> <from> <to>
  python3 - "$F" "$WORK/orig.go" "$2" "$3" <<'PY'
import sys
p,orig,f,t=sys.argv[1:5]
s=open(orig).read()
assert f in s, "mutation target not found: "+f
open(p,'w').write(s.replace(f,t,1))
PY
  out=$(run || true)
  if echo "$out" | grep -q "^ok"; then echo "FAIL: mutation '$1' SURVIVED"; exit 1; fi
  echo "$out" | grep -q "^FAIL" || { echo "$out" | tail -5; echo "FAIL: mutation '$1' was inconclusive"; exit 1; }
  echo "   killed: $1 ($(echo "$out" | grep -c -- '--- FAIL') failing tests)"
}
mut "ignore live contract/hub"         "AND EXISTS (SELECT 1 FROM live)" ""
mut "ignore the tenant of the source"  "WHERE c.tenant_id = \$2
    AND c.conversation_kind" "WHERE c.conversation_kind"
mut "project internal staff chats"     "AND c.conversation_kind <> 'internal'" ""
mut "closed conversations forever"     "OR COALESCE(lm.created_at, c.created_at) > now() - make_interval(days => \$3)" "OR true"
mut "never remove stale rows"          "AND NOT EXISTS (SELECT 1 FROM src s WHERE s.conversation_id = h.conversation_id)" "AND false"
mut "always rewrite (no change guard)" "IS DISTINCT FROM" "IS NOT DISTINCT FROM"
mut "unread ignores operator replies"  "AND o.status <> 'failed'),
                '-infinity'::timestamptz)" "AND o.status <> 'failed' AND false),
                '-infinity'::timestamptz)"
mut "priority not mapped"              "WHEN 'critical' THEN 'urgent'" "WHEN 'critical' THEN 'normal'"
mut "orphan pairs not discovered"      "UNION
			SELECT hub_id, tenant_id FROM hub_inbox_items\`)" "\`)"
cp "$WORK/orig.go" "$F"
out=$(run || true); echo "$out" | grep -q "^ok" || { echo "FAIL: suite red after restore"; exit 1; }
echo "PASS: every projector mutation was caught"
