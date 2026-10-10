#!/usr/bin/env bash
# Mutation test for THE DELEGATED AI DRAFT OF THE CLOSING (ADR-0040 phase 07): weakens each rule of the attendance route and service a delegate goes through in turn; the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubsugmut-$$; DB=omnira_test_sugmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/attendance/application/service.go internal/attendance/adapters/http.go internal/intelligence/adapters/closing_suggest.go internal/intelligence/application/closing_suggester.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=sugmut-$$" \
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
    go test -count=1 -p 1 -run 'TestDelegatedClosingDraft' \
      ./internal/hub/adapters 2>&1
  # the real suggester's holder-bound branch (a unit test: its reader and provider are fakes)
  GOFLAGS=-buildvcs=false go test -count=1 -run 'TestSuggest' ./internal/intelligence/application 2>&1
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
#   - the "no check wired" branch of the handler and the source guard of SuggestClosingAsHubAgent: a delegate only reaches them through a route that is Delegable, which exists
#     only when the check is wired, and a member's request never selects the Hub variant (the handler chooses by the access source);
#   - the "closed" rule of the service alone: the reading statement of the Hub variant carries the same condition (status <> 'closed'), so each layer alone is covered by the other
#     (both answer 422); the holder condition of the statement IS mutated, and the race test goes red without it.
AS=internal/attendance/application/service.go; AH=internal/attendance/adapters/http.go; IR=internal/intelligence/adapters/closing_suggest.go; IA=internal/intelligence/application/closing_suggester.go

mut "the route asks a key every delegate has instead of ai.assist" $AH 'suggestRoute = tenancyadapters.Delegable("ai.assist", wrap(h.suggest))' 'suggestRoute = tenancyadapters.Delegable("conversation.read", wrap(h.suggest))'
mut "the handler does not ask for conversation.read" $AH '		if !readable {' '		if false && !readable {'
mut "a delegate is routed through the member's role matrix" $AH '		suggest = h.svc.SuggestClosingAsHubAgent' '		_ = h.svc.SuggestClosingAsHubAgent'
mut "the route is delegable even when the server does not wire the check" $AH 'if h.canReadAsHubAgent != nil {
		// ADR-0040 phase 07' 'if true {
		// ADR-0040 phase 07'
mut "a delegate has the supervisor path (any conversation, not only the held one)" $AS '	if err := s.checkDraftable(ctx, tc, conversationID, false); err != nil {' '	if err := s.checkDraftable(ctx, tc, conversationID, true); err != nil {'
mut "a conversation with no contact or an internal one is drafted" $AS '	if facts.ContactID == nil || facts.Kind == "internal" {
		return domain.ErrNotAContact
	}
	if owner' '	if false && (facts.ContactID == nil || facts.Kind == "internal") {
		return domain.ErrNotAContact
	}
	if owner'
mut "the delegate's draft is not bound to the holder (service)" $AS '	return bound.SuggestClosingHeldBy(ctx, conversationID, tc.ActorID)' '	_ = bound
	return s.suggester.SuggestClosing(ctx, conversationID)'
mut "the reading statement does not require the holder" $IR '($4::uuid IS NULL OR EXISTS (SELECT 1 FROM conversations c' '(true OR EXISTS (SELECT 1 FROM conversations c'
mut "the suggester reads without the holder even when asked to bind it" $IA '	if holder != nil {
		msgs, err = s.reader.RecentMessagesHeldBy' '	if false && holder != nil {
		msgs, err = s.reader.RecentMessagesHeldBy'
mut "a delegate has no limit of paid drafts" $AS 'if s.draftAllowed != nil && !s.draftAllowed(ctx, tc.ActorID) {' 'if false && s.draftAllowed != nil && !s.draftAllowed(ctx, tc.ActorID) {'
echo "PASS: all $N mutants killed"
