#!/usr/bin/env bash
# Mutation test for THE ONE-INSTANCE PIN (ADR-0040, migration 111): weakens each restrictive policy and the lock function in turn;
# the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubpinmut-$$; DB=omnira_test_pinmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES=""
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=pinmut-$$" \
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
    go test -count=1 -p 1 -run 'TestTheDelegatedContextIsPinnedToOneInstance' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"

killed() {
  if verdict_green "$2"; then echo "SURVIVED: $1"; [ -n "${REPORT:-}" ] && return 0; echo "FAIL: mutation survived"; exit 1; fi
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
F=000111_hub_delegated_one_instance.up.sql
# NOT mutants (redundant by construction, documented in the migration): contacts and contact_account_links (second layer on the two tables a delegate can
# write; their permissive policies already hang from a visible, i.e. pinned, conversation). messages and the media tables carry no pin of their own.
for t in conversations customer_accounts; do
  sqlmut "pin of $t removed (USING true)" $F "CREATE POLICY ${t}_acting_one_instance ON $t AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());" "CREATE POLICY ${t}_acting_one_instance ON $t AS RESTRICTIVE
  USING (true);"
done
sqlmut "lock_served_tenant does not record the instance" $F "  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
" ""
sqlmut "acting_tenant reads any text as a uuid-less match (always null removed -> never null)" $F "WHEN v ~* '^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}\$' THEN v::UUID END" "WHEN v IS NOT NULL THEN v::UUID END"
echo "PASS: all $N mutants killed"
