#!/usr/bin/env bash
# Mutation test for the hub audit view (ADR-0038 §6): weakens each rule in turn (who may read, which instances, which actions, the fact whitelist,
# the filter and the paging); the real-Postgres tests must go red
# every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubauditmut-$$; DB=omnira_test_auditmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/auditlog/auditlog.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=auditmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 1 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches; -p 1: the two packages share this one database, one after the other
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -timeout 240s -run 'TestAudit_' ./internal/hub/adapters 2>&1
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
A=internal/hub/auditlog/auditlog.go
mut "anybody may read the audit"                         $A '		if !ok {
			return ErrForbidden
		}' '		if false && !ok {
			return ErrForbidden
		}'
mut "another hub's instances are listed"                 $A "WHERE (e.tenant_id IN (SELECT tenant_id FROM hub_tenant_service_contracts WHERE hub_id = \$1) OR e.metadata ->> 'hub_id' = \$1::text)" "WHERE (true OR e.metadata ->> 'hub_id' = \$1::text OR \$1::text IS NULL)"
mut "message and conversation traffic is listed"         $A "			  AND e.action NOT LIKE 'hub.message.%' AND e.action NOT LIKE 'hub.conversation.%'" ""
mut "unrelated actions are listed"                       $A "			  AND (e.action LIKE 'hub.%' OR e.action LIKE 'platform.%' OR e.action LIKE 'channel.connection_%' OR e.action LIKE 'channel.session_%')" "			  AND true"
mut "the raw record leaks (a key outside the whitelist)" $A 'var factKeys = []string{"from",' 'var factKeys = []string{"secret", "from",'
mut "the instance filter is ignored"                     $A "			  AND (\$2::uuid IS NULL OR e.tenant_id = \$2)" "			  AND (\$2::uuid IS NULL OR true)"
mut "the cursor is ignored"                              $A "			  AND (\$3::timestamptz IS NULL OR e.created_at < \$3)" "			  AND (\$3::timestamptz IS NULL OR true)"
mut "oldest first"                                       $A "ORDER BY e.created_at DESC, e.id DESC" "ORDER BY e.created_at ASC, e.id ASC"
mut "the page is not cut"                                $A 'if len(out.Items) > limit {' 'if false {'
mut "the via marker is dropped"                          $A 'if v, _ := meta["via"].(string); v == "hub" {' 'if v, _ := meta["via"].(string); v == "never" {'
echo "PASS: every mutation was killed by a failing real-Postgres test ($N mutants)"
