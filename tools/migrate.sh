#!/bin/bash
# migrate.sh — wrapper para golang-migrate

set -e

MIGRATE_VERSION="v4.17.0"
MIGRATE_BIN="migrate"

# Verificar se migrate está instalado
if ! command -v $MIGRATE_BIN &> /dev/null; then
  echo "Installing migrate CLI..."
  go install -tags 'postgres' github.com/golang-migrate/migrate/v4/cmd/migrate@$MIGRATE_VERSION
fi

# Env vars
: ${DATABASE_URL:="postgres://omnira:omnira@localhost:55434/omnira_dev?sslmode=disable"}

case "${1:-up}" in
  up)
    echo "Running migrations up..."
    $MIGRATE_BIN -path migrations -database "$DATABASE_URL" up
    ;;
  down)
    echo "Running migrations down..."
    $MIGRATE_BIN -path migrations -database "$DATABASE_URL" down
    ;;
  status)
    echo "Migration status..."
    $MIGRATE_BIN -path migrations -database "$DATABASE_URL" version
    ;;
  create)
    if [ -z "$2" ]; then
      echo "Usage: $0 create <name>"
      exit 1
    fi
    $MIGRATE_BIN create -ext sql -dir migrations -seq "$2"
    ;;
  *)
    echo "Usage: $0 {up|down|status|create <name>}"
    exit 1
    ;;
esac
