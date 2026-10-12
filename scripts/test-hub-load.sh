#!/usr/bin/env bash
# Load proof of the Hub inbox over HTTP (TestHubInboxUnderLoad): concurrent Hub agents reading a large inbox through the real handler and pool, the
# application role under FORCE RLS, the signed session context in mode enforce. Throwaway Postgres (production migrator, non-superuser app role); never production.
#
# Usage: scripts/test-hub-load.sh        (LOAD_ROWS=20000 LOAD_CLIENTS=16 LOAD_SECONDS=8 LOAD_P95_MS=1000 LOAD_COUNT_P95_MS=3000 LOAD_P99_MS=4000 CTXMODE=enforce|measure|off to change)
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-hubload-$$; DB=omnira_test_load
KEY=$(printf 'e5%.0s' $(seq 1 32))
trap 'docker rm -fv "$NAME" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=hubload-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine \
  -c shared_buffers=256MB -c max_connections=200 >/dev/null
for i in $(seq 1 120); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 0.5; done
PORT=$(docker port "$NAME" 5432/tcp | head -1 | cut -d: -f2)
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null
docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB -e OMNIRA_SESSION_CTX_KEY=$KEY -e SESSION_CTX_MODE="${CTXMODE:-enforce}" \
  postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
docker exec "$NAME" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
OMNIRA_SESSION_CTX_KEY=$KEY OMNIRA_LOAD_TEST=1 OMNIRA_INTEGRATION_TEST=1 GOFLAGS=-buildvcs=false \
  OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
  OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
  go test -count=1 -p 1 -v -timeout 20m -run 'TestHubInboxUnderLoad' ./internal/hub/adapters 2>&1 | grep -vE "^(=== RUN|go: downloading)"
