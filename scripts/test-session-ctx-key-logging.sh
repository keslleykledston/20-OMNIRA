#!/usr/bin/env bash
# ADR-0042: the signing key must never reach the database log, even when the server is configured to log every statement with its parameters (log_statement=all,
# log_min_duration_statement=0, log_parameter_max_length=-1). Throwaway Postgres; installs the key through the production migrator and through scripts/session-ctx.sh's logic,
# then greps the server log for the key. A positive control proves the log WOULD have shown it (a plain statement with the key inline).
set -euo pipefail
cd "$(dirname "$0")/.."
NAME=omnira-keylog-$$
KEY=d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5d5
CONTROL=e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6e6
trap 'docker rm -fv "$NAME" >/dev/null 2>&1 || true' EXIT
docker run -d --name "$NAME" --label com.omnira.integration-test=true --label "com.omnira.integration-test.run=keylog-$$" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=keylog postgres:16-alpine \
  -c log_statement=all -c log_min_duration_statement=0 -c log_parameter_max_length=-1 -c log_parameter_max_length_on_error=-1 -c log_transaction_sample_rate=1 >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$NAME" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
run() {
  docker run --rm --network "container:$NAME" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
    -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=keylog "$@" postgres:16-alpine sh /tools/migrate-sql.sh up
}
echo "== the server logs everything; install the key through the migrator"
run -e OMNIRA_SESSION_CTX_KEY=$KEY -e SESSION_CTX_MODE=enforce | grep -E "session context"
echo "== positive control: a plain statement with a key inline IS in the log"
docker exec "$NAME" psql -U omnira -d keylog -X -q -c "SELECT '$CONTROL'" >/dev/null
sleep 1
logs=$(docker logs "$NAME" 2>&1)
echo "$logs" | grep -q "$CONTROL" || { echo "FAIL: the control statement is not in the log, so this test proves nothing"; exit 1; }
if echo "$logs" | grep -q "$KEY"; then
  echo "FAIL: the signing key is in the database log"; echo "$logs" | grep -n "$KEY" | head -3 | cut -c1-160; exit 1
fi
echo "== the installed key works (ctx_set_mode enforce was accepted, so a key is present)"
[ "$(docker exec "$NAME" psql -U omnira -d keylog -X -At -c "SELECT mode FROM session_ctx_mode")" = enforce ] || { echo "FAIL: mode is not enforce"; exit 1; }
[ "$(docker exec "$NAME" psql -U omnira -d keylog -X -At -c "SELECT encode(key,'hex') = '$KEY' FROM session_ctx_keys WHERE state='current'")" = t ] || { echo "FAIL: the key in the database is not the one given"; exit 1; }
echo "PASS: with statement, duration and parameter logging all on, the key reached the database and not its log"
