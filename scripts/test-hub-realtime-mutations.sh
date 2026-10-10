#!/usr/bin/env bash
# Mutation test for THE DELEGATED REALTIME STREAMS (ADR-0040 phase 05): weakens each rule of the stream admission, filter and recheck in turn; the real-Postgres +
# real-NATS test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubrtmut-$$; DB=omnira_test_rtmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/inbox/adapters/sse.go internal/tenancy/adapters/serving.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" "$NATSN" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=rtmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
NATSN=omnira-hubrtmut-nats-$$; SRV=omnira-test-hubrtmut$$
docker run -d --name "$NATSN" --label com.omnira.integration-test=true -p 127.0.0.1::4222 -p 127.0.0.1::8222 nats:2.10-alpine -js -m 8222 --server_name "$SRV" >/dev/null
sleep 3
NP=$(docker port "$NATSN" 4222/tcp | head -1 | cut -d: -f2); NM=$(docker port "$NATSN" 8222/tcp | head -1 | cut -d: -f2)
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false OMNIRA_NATS_URL=nats://127.0.0.1:$NP OMNIRA_NATS_MONITOR_URL=http://127.0.0.1:$NM OMNIRA_INTEGRATION_RUN_ID=rtmut$$ OMNIRA_NATS_SERVER_NAME=$SRV \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestDelegatedRealtimeStreams' \
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
# NOT mutants (equivalent by construction):
#   - the instance-stream filter failing OPEN on a lookup error: needs a database error injected between two statements; the code fails closed and says so;
#   - ServingConversationVisible without its own conversation.read check: AuthorizeServing already asked for it at admission and asks again at every recheck;
#   - the delegated branch asking the member visibility for a conversation stream: the legacy policies give the same answer for one hub (the multi-hub case is
#     covered by the cost test, which compares the policies with the original predicate).
SSE=internal/inbox/adapters/sse.go; SV=internal/tenancy/adapters/serving.go

mut "recheck of a Hub stream is skipped (a revoked grant keeps streaming)" $SSE 'if _, err := da.AuthorizeServing(r.Context(), principal.UserID, tc.TenantID, *tc.HubID, []string{"conversation.read"}); err != nil {' 'if false {'
mut "recheck of a Hub stream uses the members' interval (an hour)" $SSE 'interval = h.opts.DelegatedRecheck' 'interval = h.opts.Recheck'
mut "the instance stream is not filtered by visibility" $SSE 'if filter != nil && !filter.allow(event.ID, time.Now()) {' 'if false && filter != nil && !filter.allow(event.ID, time.Now()) {'
mut "the visibility answer is reused forever" $SSE 'if e, hit := f.cache[id]; hit && now.Sub(e.at) < f.ttl {' 'if e, hit := f.cache[id]; hit {'
mut "a conversation stream does not recheck the queue scope" $SSE 'if visible, err := da.ConversationVisibleServing(r.Context(), principal.UserID, tc.TenantID, *tc.HubID, *onlyConversation); err != nil || !visible {' 'if false {'
mut "a malformed acting header is read as a member" $SSE 'if aerr != nil {' 'if false && aerr != nil {'
mut "the acting header is ignored (every stream is a member stream)" $SSE 'if acting.IsHub() {' 'if false && acting.IsHub() {'
mut "admission accepts a person who holds none of the keys" $SV '			if ok {
				tc = resolved
				return nil
			}' '			if ok || true {
				tc = resolved
				return nil
			}'
mut "admission ignores the process switch" $SV 'if !delegatedServing.Load() || len(keys) == 0 {
		return nil, errServeDenied
	}
	var tc *domain.TenantContext' 'if len(keys) == 0 {
		return nil, errServeDenied
	}
	var tc *domain.TenantContext'
echo "PASS: all $N mutants killed"
