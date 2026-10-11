#!/usr/bin/env bash
# Mutation test for THE CLOSING NOTICE QUEUED BY THE SERVER WITH THE FINALIZE (ADR-0040 phase 08): weakens each rule of the notice in turn; the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubfarmut-$$; DB=omnira_test_farmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/attendance/application/service.go internal/attendance/domain/attendance.go internal/hub/adapters/delegated_writes.go internal/platform/httpserver/closing_notice.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=farmut-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)

mkdb() { # mkdb <migrations dir>
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "DROP DATABASE IF EXISTS $DB WITH (FORCE)" -c "CREATE DATABASE $DB" >/dev/null
  docker run --rm --network "container:$NAME" -v "$1:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
  docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
}
run() { # the host toolchain with its warm caches: the hub path (delegate) and the member path
  OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
    OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
    OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
    go test -count=1 -p 1 -run 'TestTheDelegatedClosingNotice|TestTheClosingNotice' \
      ./internal/hub/adapters ./internal/attendance/adapters 2>&1
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
# NOT mutants (equivalent by construction): the sender's own "conversation closed" refusal when the notice is queued after the closing (a closed conversation refuses outbound text,
# so the order is observable only as an error the whole suite already treats as a failure of the finalize).
AS=internal/attendance/application/service.go; AD=internal/attendance/domain/attendance.go; DW=internal/hub/adapters/delegated_writes.go; CN=internal/platform/httpserver/closing_notice.go

mut "a member's notice is never queued" $AS '			if farewell, err = s.memberNotice(ctx, in.ConversationID, in.Farewell, farewellKey(in.ConversationID)); err != nil {
				return FinalizeResult{}, err
			}' '			farewell = domain.FarewellQueued'
mut "a Hub agent's notice is queued without the key conversation.reply" $AS '		if !mayNotify {' '		if false && !mayNotify {'
mut "the handler assumes the key conversation.reply" $DW '		mayNotify := false' '		mayNotify := true'
mut "the key check of the handler is not asked" $DW 'if in.Farewell != "" {
			if err := platformdb' 'if false && in.Farewell != "" {
			if err := platformdb'
mut "one key for every conversation (the idempotency key is not per conversation)" $AS 'func farewellKey(conversationID uuid.UUID) string { return "farewell-" + conversationID.String() }' 'func farewellKey(conversationID uuid.UUID) string { return "farewell-" + uuid.Nil.String() }'
mut "a closed window stops the finalize" $CN '	case errors.Is(err, messagesapplication.ErrWindowClosed):
		return attendancedomain.FarewellWindowClosed, nil' '	case errors.Is(err, messagesapplication.ErrWindowClosed):
		return "", err'
mut "a missing line stops the finalize" $CN '	case errors.Is(err, messagesapplication.ErrChannelUnavailable):
		return attendancedomain.FarewellNoChannel, nil' '	case errors.Is(err, messagesapplication.ErrChannelUnavailable):
		return "", err'
mut "a person who may not write to this conversation stops the finalize" $CN '	case errors.Is(err, messagesapplication.ErrForbidden), errors.Is(err, messagesapplication.ErrUnassigned), errors.Is(err, messagesapplication.ErrNotAssignedToYou):
		return attendancedomain.FarewellNotAllowed, nil' '	case errors.Is(err, messagesapplication.ErrForbidden):
		return attendancedomain.FarewellNotAllowed, nil'
mut "any other error is swallowed (the finalize goes on without the notice and without a rollback)" $CN '	return "", err
}

// ClosingNoticeSenders' '	return attendancedomain.FarewellUnavailable, nil
}

// ClosingNoticeSenders'
mut "the text of the notice is not checked for credentials" $AD '	if out.Farewell, err = checkText("farewell", in.Farewell, MaxFarewell, false); err != nil {
		return out, err
	}' '	out.Farewell = strings.TrimSpace(in.Farewell)'
mut "the audit does not name the state of the notice (member)" $AS '"follow_ups": len(items), "farewell": string(farewell),' '"follow_ups": len(items),'
echo "PASS: all $N mutants killed"
