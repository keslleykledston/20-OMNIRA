#!/bin/bash
# validate-schema.sh — validate schema structure

set -e

BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

echo -e "${BOLD}Schema Validation${RESET}\n"

# Required tables
TABLES=("users" "tenants" "memberships" "roles" "permissions" "role_permissions" "audit_events" "outbox_events")

echo "Checking tables..."
for table in "${TABLES[@]}"; do
  exists=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
    SELECT EXISTS(SELECT 1 FROM information_schema.tables WHERE table_name='$table');
  " | tr -d ' ')

  if [ "$exists" = "t" ]; then
    echo -e "  $table ... ${GREEN}✓${RESET}"
  else
    echo -e "  $table ... ${RED}✗${RESET}"
    exit 1
  fi
done

echo ""
echo "Checking RLS enabled..."
for table in "${TABLES[@]}"; do
  rls=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
    SELECT rowsecurity FROM pg_tables WHERE tablename='$table';
  " | tr -d ' ')

  if [ "$rls" = "t" ]; then
    echo -e "  $table ... ${GREEN}✓${RESET}"
  else
    echo -e "  $table ... ${RED}✗${RESET}"
    exit 1
  fi
done

echo ""
echo "Checking permissions..."
perms=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SELECT COUNT(*) FROM permissions;
" | tr -d ' ')

if [ "$perms" -gt 0 ]; then
  echo -e "  permissions inserted ($perms) ... ${GREEN}✓${RESET}"
else
  echo -e "  permissions inserted ... ${RED}✗${RESET}"
  exit 1
fi

echo ""
echo "Checking roles..."
roles=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SELECT COUNT(*) FROM roles WHERE tenant_id IS NULL;
" | tr -d ' ')

if [ "$roles" -gt 0 ]; then
  echo -e "  system roles ($roles) ... ${GREEN}✓${RESET}"
else
  echo -e "  system roles ... ${RED}✗${RESET}"
  exit 1
fi

echo ""
echo -e "${GREEN}${BOLD}Schema validation passed ✓${RESET}"
