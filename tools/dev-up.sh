#!/bin/bash
# dev-up.sh — sobe infrastructure local (docker-compose)

set -e

BOLD="\033[1m"
GREEN="\033[32m"
RESET="\033[0m"

echo -e "${BOLD}OMNIRA Development Environment${RESET}"
echo ""

# Verificar Docker
if ! command -v docker &> /dev/null; then
  echo "ERROR: Docker not found. Please install Docker."
  exit 1
fi

if ! command -v docker-compose &> /dev/null; then
  echo "ERROR: Docker Compose not found. Please install Docker Compose."
  exit 1
fi

echo "Starting services..."
docker compose up -d

echo ""
echo "Waiting for services to be healthy..."
RETRY=0
MAX_RETRIES=30
while [ $RETRY -lt $MAX_RETRIES ]; do
  if docker compose ps --services --filter "status=running" | wc -l | grep -q "3"; then
    break
  fi
  ((RETRY++))
  sleep 1
done

if [ $RETRY -eq $MAX_RETRIES ]; then
  echo "ERROR: Services failed to start within timeout"
  docker compose logs
  exit 1
fi

echo "Checking service health..."
sleep 2

# Run health check
if [ -x "$(dirname "$0")/check-docker-health.sh" ]; then
  "$(dirname "$0")/check-docker-health.sh" || exit 1
else
  echo "WARNING: health check script not found"
fi

echo ""
echo -e "${GREEN}✓ Environment ready${RESET}"
echo ""
echo "Endpoints:"
echo "  Postgres:       localhost:5432 (user: omnira, pass: omnira, db: omnira_dev)"
echo "  NATS:           localhost:4222"
echo "  OTel GRPC:      localhost:4317"
echo "  OTel HTTP:      localhost:4318"
echo "  Jaeger UI:      http://localhost:9411 (if enabled)"
echo ""
echo "Stop with: docker compose down"
echo "Logs:      docker compose logs -f [service]"
