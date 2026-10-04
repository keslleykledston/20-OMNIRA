#!/usr/bin/env bash
# Proves a migration's `down` really undoes it: applies every migration to a throwaway Postgres, rolls back the LATEST
# one, applies it again and requires the final schema to be identical to the first (pg_dump --schema-only).
# Nothing here touches the real database.
#
# Usage: scripts/test-migration-roundtrip.sh <latest-migration-name>   e.g. 000063_topic_threads
set -euo pipefail
cd "$(dirname "$0")/.."
V=${1:?usage: $0 <latest-migration-name>}
NAME=omnira-mig-rt-$$
WORK=$(mktemp -d)
cleanup() { docker rm -f "$NAME" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

docker run -d --name "$NAME" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=rt postgres:16-alpine >/dev/null
# The official image starts a temporary server for initdb and then restarts it: wait for the SECOND "ready" line.
for i in $(seq 1 90); do
  [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break
  sleep 1
done
docker exec "$NAME" pg_isready -U omnira -d rt >/dev/null || { echo "FAIL: throwaway postgres did not start"; exit 1; }

run() {
  docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=rt postgres:16-alpine sh /tools/migrate-sql.sh "$@"
}
dump() { docker exec "$NAME" pg_dump -U omnira -d rt --schema-only --no-owner --no-privileges | grep -v '^--' | grep -v '^$' | grep -v 'schema_migrations' | grep -v '^.restrict ' | grep -v '^.unrestrict '; }

echo "== up (all migrations)"
run up | tail -2
dump > "$WORK/first.sql"
echo "== down $V"
run down "$V" | tail -1
dump > "$WORK/after-down.sql"
echo "== up again"
run up | tail -2
dump > "$WORK/second.sql"

if ! diff -q "$WORK/first.sql" "$WORK/second.sql" >/dev/null; then
  echo "FAIL: schema after up/down/up differs from the first up"
  diff "$WORK/first.sql" "$WORK/second.sql" | head -30
  exit 1
fi
if diff -q "$WORK/first.sql" "$WORK/after-down.sql" >/dev/null; then
  echo "FAIL: down changed nothing (the rollback is a no-op)"
  exit 1
fi
removed=$(diff "$WORK/first.sql" "$WORK/after-down.sql" | grep -c '^<' || true)
echo "PASS: $V rolled back ($removed schema lines removed) and re-applied to an identical schema"
