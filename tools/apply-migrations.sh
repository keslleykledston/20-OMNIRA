#!/bin/bash
# apply-migrations.sh — apply SQL migrations in order

set -e

: ${DATABASE_URL:="postgres://omnira:omnira@localhost:55434/omnira_dev?sslmode=disable"}
: ${MIGRATIONS_DIR:="migrations"}

BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

# Parse connection string
parse_connstring() {
  local url="$1"
  # Simple parser for postgres://user:pass@host:port/db
  url="${url#postgres://}"
  local userpass="${url%@*}"
  local hostdb="${url#*@}"

  export PGUSER="${userpass%:*}"
  export PGPASSWORD="${userpass#*:}"
  export PGHOST="${hostdb%:*}"
  export PGPORT="${hostdb#*:}"
  export PGPORT="${PGPORT%/*}"
  export PGDATABASE="${hostdb#*/}"
}

migrate_up() {
  echo -e "${BOLD}Applying migrations...${RESET}"

  # Sort and apply .up.sql files
  for f in "$MIGRATIONS_DIR"/*.up.sql; do
    if [ -f "$f" ]; then
      name=$(basename "$f")
      echo -n "  $name ... "

      if psql -q -f "$f" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${RESET}"
      else
        echo -e "${RED}✗${RESET}"
        echo "Failed to apply $f"
        exit 1
      fi
    fi
  done
}

migrate_down() {
  echo -e "${BOLD}Rolling back migrations...${RESET}"

  # Sort and apply .down.sql files in reverse order
  for f in $(ls -r "$MIGRATIONS_DIR"/*.down.sql 2>/dev/null); do
    if [ -f "$f" ]; then
      name=$(basename "$f")
      echo -n "  $name ... "

      if psql -q -f "$f" > /dev/null 2>&1; then
        echo -e "${GREEN}✓${RESET}"
      else
        echo -e "${RED}✗${RESET}"
        echo "Failed to rollback $f"
        exit 1
      fi
    fi
  done
}

validate_schema() {
  echo -e "${BOLD}Validating schema...${RESET}"

  # Check if key tables exist
  local tables=("users" "tenants" "memberships" "roles" "permissions" "audit_events" "outbox_events")
  for table in "${tables[@]}"; do
    if psql -t -c "SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='$table');" | grep -q 't'; then
      echo -e "  $table ... ${GREEN}✓${RESET}"
    else
      echo -e "  $table ... ${RED}✗${RESET}"
      return 1
    fi
  done

  echo -e "${GREEN}Schema validation passed${RESET}"
}

main() {
  parse_connstring "$DATABASE_URL"

  case "${1:-up}" in
    up)
      migrate_up
      validate_schema
      ;;
    down)
      migrate_down
      ;;
    validate)
      validate_schema
      ;;
    *)
      echo "Usage: $0 {up|down|validate}"
      exit 1
      ;;
  esac
}

main "$@"
