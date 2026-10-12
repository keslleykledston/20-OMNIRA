#!/usr/bin/env bash
# Runs ONE integration package (optionally one test) against a throwaway Postgres (production migrator, signed session context in mode enforce) AND a throwaway
# NATS/JetStream, so a package that needs NATS can be proven without the 37-package gate. Same guards as scripts/test-integration.sh (loopback, labeled, attested
# server name). Never touches the development or production services.
#
# Usage: scripts/test-integration-one.sh <go package> [test name regex]      e.g. scripts/test-integration-one.sh ./internal/worker/realtime TestBridge
#        CTXMODE=off|measure|enforce (default enforce)   VERBOSE=1
set -euo pipefail
cd "$(dirname "$0")/.."
PKG=${1:?go package, e.g. ./internal/worker/realtime}; RUN=${2:-.}
ID=$$; PG=omnira-test-pg-one-$ID; NATS=omnira-test-nats-one-$ID; DB=omnira_test_one; SN=omnira-test-one-$ID
KEY=$(printf 'f6%.0s' $(seq 1 32))
trap 'docker rm -fv "$PG" "$NATS" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$PG" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=one-$ID" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=postgres -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
docker run -d --name "$NATS" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=one-$ID" \
  -p 127.0.0.1::4222 -p 127.0.0.1::8222 nats:2.10-alpine -js -m 8222 --server_name "$SN" >/dev/null
for i in $(seq 1 120); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 0.5; done
PORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2)
NPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2); MPORT=$(docker port "$NATS" 8222/tcp | head -1 | cut -d: -f2)
for i in $(seq 1 60); do curl -sf "http://127.0.0.1:$MPORT/healthz" >/dev/null 2>&1 && break; sleep 0.3; done
docker exec "$PG" psql -U omnira -d postgres -X -q -c "CREATE DATABASE $DB" >/dev/null
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=$DB -e OMNIRA_SESSION_CTX_KEY=$KEY -e SESSION_CTX_MODE="${CTXMODE:-enforce}" \
  postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1
docker exec "$PG" psql -U omnira -d postgres -X -q -c "GRANT CONNECT ON DATABASE $DB TO omnira_app" >/dev/null
OMNIRA_SESSION_CTX_KEY=$KEY OMNIRA_INTEGRATION_TEST=1 OMNIRA_INTEGRATION_RUN_ID="one-$ID" GOFLAGS=-buildvcs=false \
  OMNIRA_DATABASE_URL="postgres://omnira:pw@127.0.0.1:$PORT/$DB?sslmode=disable" \
  OMNIRA_APP_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PORT/$DB?sslmode=disable" \
  OMNIRA_NATS_URL="nats://127.0.0.1:$NPORT" OMNIRA_NATS_MONITOR_URL="http://127.0.0.1:$MPORT" OMNIRA_NATS_SERVER_NAME="$SN" \
  go test -count=1 -p 1 ${VERBOSE:+-v} -run "$RUN" "$PKG" 2>&1 | grep -v "^go: downloading"
