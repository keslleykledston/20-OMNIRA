#!/usr/bin/env bash
# Mutation test for THE CHEAP POLICIES OF THE DELEGATED CONTEXT (ADR-0040 phase 04d, migration 114): weakens each rule of the rewritten policies in turn; the
# real-Postgres tests must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubcostmut-$$; DB=omnira_test_costmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES=""
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=costmut-$$" \
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
    go test -count=1 -p 1 -run 'TestTheCheapPolicies|TestTheDelegatedTransactionDoesNotJIT|TestDelegatedNotesFollow|TestOneHubNeverReads|TestAPersonServedByTwoHubs|TestAMemberWhoIsAlsoADelegate' \
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
sqlmut() { # sqlmut <name> <migration file> <from> <to> [all]   (all: replace every occurrence, for a rule written in two layers)
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ]; then echo "   skipped: $1"; return 0; fi
  rm -rf "$WORK/mig"; mkdir -p "$WORK/mig"; cp migrations/*.sql "$WORK/mig/"
  python3 - "$WORK/mig/$2" "$3" "$4" "${5:-}" <<'PY'
import sys
p,f,t,every=sys.argv[1:5]; s=open(p).read()
assert f in s, "SQL mutation target not found: "+f
open(p,'w').write(s.replace(f,t) if every == "all" else s.replace(f,t,1))
PY
  mkdb "$WORK/mig"
  killed "$1" "$(run || true)"
}
# NOT mutants (equivalent by construction; another layer gives the same answer, so no test could tell them apart):
#   - acting_scope without the liveness conditions (grant / contract / hub / company / account active and in window), without the hub or instance filter, or
#     live_any forced true: the only door into the context, lock_served_tenant, pins and re-checks the very same conditions in the same transaction, so inside the
#     context they always hold (the test covers each state: the context cannot be entered);
#   - conversations_read_acting without the instance test, or without the scope while the restrictive policy keeps it (and the reverse): the two policies and the
#     pin say the same thing; only removing the scope from BOTH changes what is seen (a mutant below);
#   - messages_acting_hub_only forced true: messages_read_acting already asks the same;
#   - the legacy policies gated by "not in the delegated context" and the members' inline gating: pure cost changes (the answer is the same either way:
#     the restrictive policies already forbade the sum of hubs, and has_active_membership() is false while acting); scripts/bench-hub-policies.sh measures them.
M=000114_hub_delegated_policy_cost.up.sql
sqlmut "acting_scope: a contract scope without a queue list is not unrestricted (nobody sees anything unscoped)" $M "COALESCE(bool_or(sc -> 'queue_ids' IS NULL), false)," "false,"
sqlmut "acting_scope: every contract is unrestricted" $M "COALESCE(bool_or(sc -> 'queue_ids' IS NULL), false)," "true,"
sqlmut "acting_scope: a non-array queue list is read as a list" $M "                   WHERE jsonb_typeof(p2.sc -> 'queue_ids') = 'array'), '{}'::text[])" "                   WHERE true), '{}'::text[])"
sqlmut "acting_scope: another hub's contract counts (the hub filter is removed AND the lock cannot save it: two hubs, same person)" $M "JOIN public.effective_access_grants g ON g.user_id = me.uid AND g.tenant_id = me.tenant AND g.hub_id = me.hub" "JOIN public.effective_access_grants g ON g.user_id = me.uid AND g.tenant_id = me.tenant"
sqlmut "conversations: the queue scope is ignored in BOTH the permissive and the restrictive policy" $M "AND ((SELECT s.unrestricted FROM acting_scope() s) OR queue_id::text = ANY (COALESCE((SELECT s.queue_texts FROM acting_scope() s), '{}'::text[]))))" "AND true)" all
sqlmut "messages: a message is visible without its conversation being visible" $M "         AND (SELECT s.live_any FROM acting_scope() s)
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = messages.conversation_id));
ALTER POLICY messages_read_hub_delegation" "         AND (SELECT s.live_any FROM acting_scope() s));
ALTER POLICY messages_read_hub_delegation"
sqlmut "notes: the key is not asked any more" $M "         AND (SELECT actor_has_permission(acting_tenant(), current_user_id(), 'contact.note'))
" "         AND true
"
sqlmut "the delegated transaction JIT-compiles again" $M "  PERFORM set_config('jit', 'off', true);" "  NULL;"
echo "PASS: all $N mutants killed"
