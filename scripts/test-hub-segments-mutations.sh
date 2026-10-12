#!/usr/bin/env bash
# Mutation test for THE HUB INBOX SEGMENTS (ADR-0040 §24, no migration): weakens each rule of the segment filter, the search, the counters of the tabs and the
# assignment in turn; the real-Postgres test must go red every time and green on the original. A mutant only counts as killed by a failing TEST (a build error is not a kill).
# Throwaway database only. Never edit this file while it runs (bash reads it incrementally). SKIP=n resumes.
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubsegmut-$$; DB=omnira_test_segmut
WORK=$(mktemp -d); mkdir -p "$WORK/orig" "$WORK/mig"
FILES="internal/hub/adapters/postgres.go internal/hub/adapters/http.go"
for f in $FILES; do mkdir -p "$WORK/orig/$(dirname "$f")"; cp "$f" "$WORK/orig/$f"; done
cleanup() { for f in $FILES; do cp "$WORK/orig/$f" "$f"; done; docker rm -fv "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=segmut-$$" \
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
    go test -count=1 -p 1 -run 'TestHubInbox_Segments|TestHubInbox_Companies|TestHubInbox_TheQueue' \
      ./internal/hub/adapters 2>&1
}
verdict_green() { echo "$1" | grep -q "^FAIL" && return 1; [ "$(echo "$1" | grep -c '^ok')" -ge 1 ]; }

if [ -z "${DRY:-}" ]; then
mkdb "$PWD/migrations"
out=$(run || true); verdict_green "$out" || { echo "$out" | tail -25; echo "FAIL: baseline is red"; exit 1; }
echo "== baseline green"
fi

killed() {
  if verdict_green "$2"; then echo "SURVIVED: $1"; [ -n "${REPORT:-}" ] && return 0; echo "FAIL: mutation survived"; exit 1; fi
  [ "$(echo "$2" | grep -c -- '--- FAIL')" -ge 1 ] || { echo "$2" | tail -12; echo "FAIL: mutation '$1' was inconclusive (no failing test; build error?)"; exit 1; }
  echo "   killed: $1 ($(echo "$2" | grep -c -- '--- FAIL') failing tests)"
}
N=0
mut() { # mut <name> <file> <from> <to>
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ] || { [ -n "${ONLY:-}" ] && [ "$N" -ne "$ONLY" ]; }; then echo "   skipped: $1"; return 0; fi
  python3 - "$WORK/orig" "$2" "$3" "$4" "${DRY:-}" <<'PY'
import sys
orig,f,a,b,dry=sys.argv[1:6]
s=open(orig+"/"+f).read()
assert a in s, "mutation target not found: "+a
if not dry: open(f,'w').write(s.replace(a,b,1))
PY
  [ -n "${DRY:-}" ] && { echo "   target found: $1"; return 0; }
  killed "$1" "$(run || true)"
  cp "$WORK/orig/$2" "$2"
}
sqlmut() { # sqlmut <name> <migration file> <from> <to> [all]   (all: replace every occurrence, for a rule written in two layers)
  N=$((N+1)); if [ "$N" -le "${SKIP:-0}" ] || { [ -n "${ONLY:-}" ] && [ "$N" -ne "$ONLY" ]; }; then echo "   skipped: $1"; return 0; fi
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
PG=internal/hub/adapters/postgres.go; HT=internal/hub/adapters/http.go
echo "== mutants"
mut "waiting also lists what somebody holds" $PG "AND COALESCE(status, 'open') <> 'closed' AND assigned_user_id IS NULL" "AND COALESCE(status, 'open') <> 'closed'"
mut "mine lists what any person holds" $PG "AND assigned_user_id = \$%d\`, len(args))" "AND (assigned_user_id = \$%d OR assigned_user_id IS NOT NULL)\`, len(args))"
mut "open lists the finalized too" $PG "case ports.SegmentOpen:
		query += \` AND COALESCE(status, 'open') <> 'closed'\`" "case ports.SegmentOpen:"
mut "closed lists the open ones" $PG "query += \` AND status = 'closed'\`" "query += \` AND status <> 'closed'\`"
mut "an unknown segment becomes everything" $PG "default:
		return nil, \"\", ErrInvalidSegment" "default:"
mut "the search ignores the text" $PG "AND customer_name ILIKE \$%d\`" "AND (customer_name ILIKE \$%d OR true)\`"
mut "the search treats % and _ as wildcards" $PG "strings.NewReplacer(\`\\\`, \`\\\\\`, \`%\`, \`\\%\`, \`_\`, \`\\_\`)" "strings.NewReplacer(\`\\\`, \`\\\\\`)"
mut "the counter of mine counts every held" $PG "assigned_user_id = \$3)," "(assigned_user_id = \$3 OR assigned_user_id IS NOT NULL)),"
mut "the counters see nothing without a company filter" $PG "COALESCE(cardinality(\$2::uuid[]), 0) = 0 OR" "cardinality(\$2::uuid[]) = 0 OR"
mut "a mistyped query string is not refused" $HT "if _, err := url.ParseQuery(r.URL.RawQuery); err != nil {" "if _, err := url.ParseQuery(r.URL.RawQuery); err != nil && false {"
mut "the assignment says 'me' for everybody" $HT "if *i.AssignedUserID == actor {" "if *i.AssignedUserID != actor {"
mut "the counters repeat on every page" $HT "if include[\"counts\"] && opts.Cursor == \"\" {" "if include[\"counts\"] {"
mut "the counters are computed without being asked for" $HT "if include[\"counts\"] && opts.Cursor == \"\" {" "if opts.Cursor == \"\" {"
mut "the company counters are computed without being asked for" $HT "include[\"company_counts\"])}" "true)}"
mut "an unknown include is accepted" $HT "if part != \"counts\" && part != \"company_counts\" {" "if false {"
mut "an over-long search is accepted" $HT "if len([]rune(query)) > 80 {" "if len([]rune(query)) > 800000 {"
mut "the tab counts the finalized" $HT "count(*) FILTER (WHERE COALESCE(i.status, 'open') <> 'closed')::int AS open," "count(*)::int AS open,"
mut "the unanswered badge counts every open one" $HT "AND COALESCE(i.unread_count, 0) > 0)::int AS unanswered" "AND COALESCE(i.unread_count, 0) >= 0)::int AS unanswered"
mut "the holder is read from the lagging projection" $PG "CASE WHEN c.id IS NULL THEN i.assigned_user_id ELSE c.assigned_to_user_id END AS assigned_user_id" "i.assigned_user_id AS assigned_user_id"
mut "the finalized state is read from the lagging projection" $PG "CASE WHEN c.id IS NULL THEN i.status ELSE c.status END AS status" "i.status AS status"
mut "opening an item reads the lagging projection" $PG "FROM \`+liveInbox+\` li WHERE hub_id = \$1 AND id = \$2\`, hubID, itemID" "FROM hub_inbox_items WHERE hub_id = \$1 AND id = \$2\`, hubID, itemID"
mut "the queue is read from the lagging projection" $PG "CASE WHEN c.id IS NULL THEN i.queue_id ELSE c.queue_id END AS queue_id," "i.queue_id AS queue_id,"
echo "== done: $N mutants"
