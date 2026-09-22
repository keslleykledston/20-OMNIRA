#!/usr/bin/env bash
# Real-stack browser e2e for the Inbox vertical (Playwright + chromium).
# Brings up: throwaway DB (omnira_e2e) + seed, a throwaway WAHA, API and worker containers (app role omnira_app,
# runtime guard on), the built SPA via `vite preview` (proxying /api), then runs
# web/e2e/inbox.spec.ts and web/e2e/channels.spec.ts (the latter drives a real, throwaway WAHA), plus a
# throwaway Valkey (presence, IAM4.2-A) on its own port — never the dev stack's, if any. Requires
# something reachable at 127.0.0.1:55434 (Postgres) and 127.0.0.1:4222 (NATS) — any
# container/compose project, auto-detected by port (see resolve_pg_container below),
# docker, node/npm (web/node_modules installed) and Playwright's chromium.
#   scripts/e2e-inbox.sh            run and tear down
#   scripts/e2e-inbox.sh --keep     leave everything up afterwards
set -uo pipefail
cd "$(dirname "$0")/.."
KEEP=0; [ "${1:-}" = "--keep" ] && KEEP=1
DB=omnira_e2e; API_PORT=28961; WEB_PORT=4173; WAHA_PORT=23200; SMTP_PORT=12525; WAHA_KEY=e2ekey; PG_PORT=55434; NATS_PORT="${E2E_NATS_PORT:-4222}"; VALKEY_PORT="${E2E_VALKEY_PORT:-26379}"
# This script only needs (a) a psql-capable exec target for the fixed-port Postgres
# and (b) NATS reachable on its fixed port — not any particular Compose project or
# container_name. Resolve the Postgres container by which one actually publishes
# 127.0.0.1:55434 (works for docker-compose.yml's "omnira-postgres" and for
# docker-compose.prod.yml's project-prefixed name alike); NATS only needs the port
# open, no exec. E2E_PG_CONTAINER/E2E_NATS_PORT remain explicit overrides.
resolve_pg_container() {
  [ -n "${E2E_PG_CONTAINER:-}" ] && { echo "$E2E_PG_CONTAINER"; return; }
  docker ps --format '{{.Names}}\t{{.Ports}}' | awk -v p=":$PG_PORT->" '$0 ~ p {print $1; exit}'
}
PG_CONTAINER="$(resolve_pg_container)"
PSQL=(docker exec -i "$PG_CONTAINER" psql -U omnira -v ON_ERROR_STOP=1 -q)
PREVIEW_PID=""
cleanup() {
  [ "$KEEP" = 1 ] && { echo "--keep: stack left running (DB $DB, API :$API_PORT, web :$WEB_PORT)"; return; }
  [ -n "$PREVIEW_PID" ] && kill "$PREVIEW_PID" 2>/dev/null
  docker rm -f omnira-e2e-api omnira-e2e-worker omnira-e2e-waha omnira-e2e-valkey >/dev/null 2>&1
  "${PSQL[@]}" -d postgres -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
}
trap cleanup EXIT
die() { echo "ERROR: $*" >&2; exit 1; }

[ -n "$PG_CONTAINER" ] || die "no container publishes 127.0.0.1:$PG_PORT (set E2E_PG_CONTAINER to override)"
(exec 3<>"/dev/tcp/127.0.0.1/$NATS_PORT") 2>/dev/null || die "nothing reachable at 127.0.0.1:$NATS_PORT (NATS; set E2E_NATS_PORT to override)"
[ -d web/node_modules ] || die "run npm ci in web/ first"

echo "== database"
"${PSQL[@]}" -d postgres -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB" >/dev/null || die "create db"
for f in migrations/*.up.sql; do "${PSQL[@]}" -d $DB < "$f" >/dev/null 2>/tmp/e2e.err || { cat /tmp/e2e.err; die "migration $f"; }; done
"${PSQL[@]}" -d $DB < web/e2e/fixtures.sql >/dev/null || die "seed"

echo "== images and containers"
docker build -q -f Dockerfile.api -t omnira-api:e2e . >/dev/null || die "api build"
docker build -q -f Dockerfile.worker -t omnira-worker:e2e . >/dev/null || die "worker build"
docker rm -f omnira-e2e-api omnira-e2e-worker omnira-e2e-waha omnira-e2e-valkey >/dev/null 2>&1
docker image inspect devlikeapro/waha:gows-2026.8.2 >/dev/null 2>&1 || docker pull -q devlikeapro/waha:gows-2026.8.2 >/dev/null || die "cannot get the WAHA image"
docker run -d --name omnira-e2e-waha -p 127.0.0.1:$WAHA_PORT:3000 --add-host=host.docker.internal:host-gateway \
  -e WAHA_API_KEY=$WAHA_KEY -e WHATSAPP_DEFAULT_ENGINE=GOWS -e WAHA_DASHBOARD_ENABLED=false -e WHATSAPP_SWAGGER_ENABLED=false \
  devlikeapro/waha:gows-2026.8.2 >/dev/null || die "waha start"
for i in $(seq 1 40); do curl -sf -o /dev/null -H "X-Api-Key: $WAHA_KEY" localhost:$WAHA_PORT/health && break; sleep 1; done
# Presence (IAM4.2-A): its own throwaway Valkey, never the dev stack's — ephemeral by design,
# so a fresh instance per run is correct, not a shortcut.
docker image inspect valkey/valkey:8-alpine >/dev/null 2>&1 || docker pull -q valkey/valkey:8-alpine >/dev/null || die "cannot get the Valkey image"
docker run -d --name omnira-e2e-valkey -p 127.0.0.1:$VALKEY_PORT:6379 valkey/valkey:8-alpine >/dev/null || die "valkey start"
for i in $(seq 1 20); do docker exec omnira-e2e-valkey valkey-cli ping >/dev/null 2>&1 && break; sleep 1; done
docker exec omnira-e2e-valkey valkey-cli ping >/dev/null 2>&1 || die "valkey not healthy"
KEY=$(head -c 32 /dev/urandom | base64)
# OMNIRA_ENV defaults to "development" (config.go), which allows dev auth, but the
# route only registers when OMNIRA_DEV_AUTH_ENABLED=true is explicit (IAM1 hardening:
# neither a missing OMNIRA_ENV nor a forgotten flag alone opens the door). Without it
# /login never renders the email field ("no auth method configured") and every test
# that logs in times out waiting for a field that doesn't exist.
ENVS=(-e OMNIRA_CREDENTIALS_KEY="$KEY" -e OMNIRA_NATS_URL="nats://127.0.0.1:$NATS_PORT"
      -e OMNIRA_VALKEY_URL="redis://127.0.0.1:$VALKEY_PORT/0"
      -e OMNIRA_ENV=development -e OMNIRA_DEV_AUTH_ENABLED=true
      -e OMNIRA_WAHA_ENABLED=true -e OMNIRA_WAHA_BASE_URL=http://127.0.0.1:$WAHA_PORT -e OMNIRA_WAHA_API_KEY=$WAHA_KEY
      -e OMNIRA_PUBLIC_BASE_URL=http://host.docker.internal:$API_PORT
      # invitation e-mail: the invitation-email spec runs its own plaintext SMTP sink on this port
      -e OMNIRA_WEB_BASE_URL=http://127.0.0.1:$WEB_PORT -e OMNIRA_SMTP_HOST=127.0.0.1 -e OMNIRA_SMTP_PORT=$SMTP_PORT
      -e OMNIRA_SMTP_TLS=none -e OMNIRA_SMTP_FROM=no-reply@omnira.test
      -e OMNIRA_DATABASE_URL="postgres://omnira_app:omnira_app@127.0.0.1:$PG_PORT/$DB?sslmode=disable")
docker run -d --name omnira-e2e-api --network host -e OMNIRA_HTTP_ADDR=127.0.0.1:$API_PORT "${ENVS[@]}" omnira-api:e2e >/dev/null || die "api start"
docker run -d --name omnira-e2e-worker --network host "${ENVS[@]}" omnira-worker:e2e >/dev/null || die "worker start"
for i in $(seq 1 30); do curl -sf -o /dev/null "http://127.0.0.1:$API_PORT/healthz" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$API_PORT/healthz" || { docker logs omnira-e2e-api 2>&1 | tail -5; die "API not healthy"; }

echo "== web"
(cd web && npx vite build >/tmp/e2e-web-build.log 2>&1) || { tail -20 /tmp/e2e-web-build.log; die "vite build"; }
(cd web && VITE_API_PROXY="http://127.0.0.1:$API_PORT" npx vite preview --port $WEB_PORT --host 127.0.0.1 --strictPort >/tmp/e2e-web-preview.log 2>&1) &
PREVIEW_PID=$!
for i in $(seq 1 30); do curl -sf -o /dev/null "http://127.0.0.1:$WEB_PORT/" && break; sleep 1; done
curl -sf -o /dev/null "http://127.0.0.1:$WEB_PORT/" || { cat /tmp/e2e-web-preview.log; die "preview not up"; }

echo "== playwright"
PLAYWRIGHT_ARGS=()
[ -n "${E2E_PLAYWRIGHT_GREP:-}" ] && PLAYWRIGHT_ARGS=(--grep "$E2E_PLAYWRIGHT_GREP")
(cd web && E2E_DB=$DB E2E_PG_CONTAINER="$PG_CONTAINER" E2E_WAHA_URL=http://127.0.0.1:$WAHA_PORT E2E_WAHA_KEY=$WAHA_KEY E2E_API_URL=http://127.0.0.1:$API_PORT E2E_SMTP_PORT=$SMTP_PORT E2E_BASE_URL="http://127.0.0.1:$WEB_PORT" npx playwright test -c playwright.inbox.config.ts "${PLAYWRIGHT_ARGS[@]}")
RC=$?
[ $RC -ne 0 ] && { echo "--- api logs"; docker logs omnira-e2e-api 2>&1 | tail -15; echo "--- worker logs"; docker logs omnira-e2e-worker 2>&1 | grep -v '^published' | tail -8; echo "--- valkey logs"; docker logs omnira-e2e-valkey 2>&1 | tail -8; }
exit $RC
