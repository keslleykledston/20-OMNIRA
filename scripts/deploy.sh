#!/usr/bin/env bash
# PILOT.3: deploy the OMNIRA application (frontend, API, worker, forward
# migrations only). Never restarts shared infrastructure (Postgres, NATS,
# Valkey, WAHA, Keycloak) — a WAHA restart risks the real, connected
# WhatsApp session, and none of the others are part of an application
# release. See docs/operations/PILOT-RUNBOOK.md for rollback and the
# forward-only migration policy.
#
# Usage: scripts/deploy.sh
set -euo pipefail
cd "$(dirname "$0")/.."

SHA=$(git rev-parse --short HEAD)
echo "== deploy starting: $SHA ($(date -u +%FT%TZ))"

echo "== checking working tree is clean"
# Images are tagged by commit SHA — deploying a dirty tree would tag
# uncommitted changes as if they were that exact commit, making the tag a
# lie. docker-compose.override.yml is the one known machine-local exception
# (never committed, never should be).
DIRTY=$(git status --porcelain | grep -v '^?? docker-compose\.override\.yml$' || true)
if [ -n "$DIRTY" ]; then
  echo "refusing to deploy: working tree is not clean" >&2
  echo "$DIRTY" >&2
  echo "commit or stash changes before deploying" >&2
  exit 1
fi

echo "== checking prerequisites"
command -v docker >/dev/null || { echo "docker not found" >&2; exit 1; }
docker compose version >/dev/null 2>&1 || { echo "docker compose (v2 plugin) not found" >&2; exit 1; }
[ -f .env ] || { echo ".env not found — see docs/operations/PILOT-RUNBOOK.md" >&2; exit 1; }

echo "== frontend: tests + typecheck + production build"
(cd web && npm ci && npm test -- --run && npm run build)

echo "== building images: api worker web"
docker compose build api worker web

echo "== tagging images with $SHA (keeps the previous build available for rollback)"
for img in 20-omnira-api 20-omnira-worker 20-omnira-web; do
  docker tag "$img:latest" "$img:$SHA" 2>/dev/null || true
done

echo "== validating compose config"
docker compose config >/dev/null

echo "== applying migrations (forward only — never migrate down here)"
docker compose up migrate

echo "== recreating application services (api, worker, web) — infra untouched"
docker compose up -d --force-recreate api
docker compose up -d --force-recreate worker
docker compose up -d --force-recreate web

echo "== health gate"
for svc in omnira-api omnira-worker omnira-web; do
  printf '%s: ' "$svc"
  ok=""
  for _ in $(seq 1 30); do
    status=$(docker inspect -f '{{.State.Health.Status}}' "$svc" 2>/dev/null || echo "unknown")
    if [ "$status" = "healthy" ]; then ok=1; echo "healthy"; break; fi
    sleep 2
  done
  [ -n "$ok" ] || { echo "FAILED (last status: $status)" >&2; exit 1; }
done

echo "== public smoke"
curl -sf -o /dev/null https://omnira.devops.k3gsolutions.com.br/ \
  || { echo "public app FAILED" >&2; exit 1; }
curl -sf -o /dev/null https://auth.devops.k3gsolutions.com.br/realms/omnira/.well-known/openid-configuration \
  || { echo "public auth issuer FAILED" >&2; exit 1; }

echo "== deploy OK: $SHA"
echo "== rollback (if needed): see docs/operations/PILOT-RUNBOOK.md \"Application rollback\""
