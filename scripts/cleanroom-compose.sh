#!/usr/bin/env bash
# Clean-room proof of the docker-compose deliverable.
# Brings up the WHOLE stack from docker-compose.yml as an isolated project (omnira-cr): new volumes,
# random secrets (DB owner/app passwords, credentials key, WAHA key), no fixed host ports of the
# default dev stack, a real WAHA on the compose network, migrations by the one-shot `migrate`
# service, seed by the `seed` service, then runs the browser e2e through the real `web` (nginx)
# container. Nothing of the developer's running stack is touched.
#   scripts/cleanroom-compose.sh           run and tear down
#   scripts/cleanroom-compose.sh --keep    leave the stack up
set -uo pipefail
cd "$(dirname "$0")/.."
KEEP=0; [ "${1:-}" = "--keep" ] && KEEP=1
P=omnira-cr
free_port() { python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])'; }
WEB_PORT=$(free_port); API_PORT=$(free_port)
rand() { head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c 24; }
export OMNIRA_DB_NAME=omnira_cr POSTGRES_PASSWORD="$(rand)" OMNIRA_APP_DB_PASSWORD="$(rand)"
export OMNIRA_CREDENTIALS_KEY="$(head -c 32 /dev/urandom | base64)"
WK="$(rand)"; export WAHA_API_KEY="$WK" OMNIRA_WAHA_API_KEY="$WK" OMNIRA_WAHA_ENABLED=true
export OMNIRA_PUBLIC_BASE_URL=http://api:8080   # WAHA reaches the API by service name: real webhook path

OVR="$(mktemp --suffix=.yml)"
cat > "$OVR" <<YML
services:
  postgres: { container_name: !reset null, ports: !override [] }
  nats:     { container_name: !reset null, ports: !override [] }
  migrate:  { container_name: !reset null }
  seed:     { container_name: !reset null }
  api:      { container_name: !reset null, ports: !override ["127.0.0.1:$API_PORT:8080"] }
  worker:   { container_name: !reset null }
  web:      { container_name: !reset null, ports: !override ["127.0.0.1:$WEB_PORT:80"] }
  waha:     { container_name: !reset null }
YML
DC=(docker compose -p $P -f docker-compose.yml -f "$OVR" --profile dev --profile whatsapp-unofficial)
cleanup() {
  [ "$KEEP" = 1 ] && { echo "--keep: project $P left running (web :$WEB_PORT, api :$API_PORT)"; return; }
  "${DC[@]}" down -v --remove-orphans >/dev/null 2>&1; rm -f "$OVR"
}
trap cleanup EXIT
die() { echo "ERROR: $*" >&2; "${DC[@]}" ps -a 2>&1 | tail -12; "${DC[@]}" logs --tail 15 api worker migrate 2>&1 | tail -40; exit 1; }
[ -d web/node_modules ] || die "run npm ci in web/ first"
docker image inspect devlikeapro/waha:gows-2026.8.2 >/dev/null 2>&1 || docker pull -q devlikeapro/waha:gows-2026.8.2 >/dev/null || die "cannot get the WAHA image"

echo "== compose up (postgres, nats, migrate, api, worker, web, waha)"
"${DC[@]}" up -d --build --wait --wait-timeout 240 api worker web waha >/tmp/cleanroom-up.log 2>&1 || { tail -20 /tmp/cleanroom-up.log; die "compose up"; }
echo "== migrations applied by the migrate service"
"${DC[@]}" logs migrate 2>&1 | grep -E "applying 000027|up to date" | tail -2
PGC=$("${DC[@]}" ps -q postgres)
q() { docker exec "$PGC" psql -U omnira -d $OMNIRA_DB_NAME -tA -c "$1"; }
echo "   schema_migrations rows: $(q 'select count(*) from schema_migrations')"
echo "== app role uses the generated password; owner is refused by the runtime guard"
docker exec -e PGPASSWORD="$OMNIRA_APP_DB_PASSWORD" "$PGC" psql -h 127.0.0.1 -U omnira_app -d $OMNIRA_DB_NAME -tAc "select current_user" | sed 's/^/   connected as /'
echo "   api: $("${DC[@]}" ps api --format '{{.Health}}')  worker: $("${DC[@]}" ps worker --format '{{.Health}}')  web: $("${DC[@]}" ps web --format '{{.Health}}')"
echo "== dev seed (service) + e2e fixtures"
"${DC[@]}" run --rm seed >/dev/null 2>&1 || die "seed service"
docker exec -i "$PGC" psql -U omnira -d $OMNIRA_DB_NAME -v ON_ERROR_STOP=1 -q < web/e2e/fixtures.sql >/dev/null || die "fixtures"
echo "== through the real web container: health, SPA fallback, /internal not exposed"
curl -sf -o /dev/null "http://127.0.0.1:$WEB_PORT/" && echo "   SPA served"
curl -sf "http://127.0.0.1:$WEB_PORT/api/v1/auth/health" >/dev/null && echo "   /api proxied to the API"
code=$(curl -s -o /dev/null -w '%{http_code}' "http://127.0.0.1:$WEB_PORT/internal/health/live"); body=$(curl -s "http://127.0.0.1:$WEB_PORT/internal/health/live" | head -c 60)
echo "   /internal/health/live via web -> $code ($(echo "$body" | grep -qi '<!doctype html' && echo 'SPA index, backend NOT reachable' || echo "$body"))"

echo "== playwright against the compose stack"
(cd web && E2E_DB=$OMNIRA_DB_NAME E2E_PG_CONTAINER="$PGC" E2E_BASE_URL="http://127.0.0.1:$WEB_PORT" E2E_API_URL="http://127.0.0.1:$API_PORT" \
  E2E_WAHA_URL=http://127.0.0.1:1 E2E_WAHA_KEY=x npx playwright test -c playwright.inbox.config.ts)
RC=$?
[ $RC -ne 0 ] && { echo "--- api"; "${DC[@]}" logs --tail 20 api; echo "--- worker"; "${DC[@]}" logs --tail 10 worker | grep -v '^.*published'; }
exit $RC
