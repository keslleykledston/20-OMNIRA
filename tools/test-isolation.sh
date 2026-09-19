#!/bin/bash
# test-isolation.sh — basic isolation test (A vs B tenant)

set -e

: ${DATABASE_URL:="postgres://omnira:omnira@localhost:55434/omnira_dev?sslmode=disable"}

BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

echo -e "${BOLD}Isolation Test (A vs B Tenant)${RESET}\n"

# Create test data via docker exec
docker exec omnira-postgres psql -U omnira -d omnira_dev << 'SQL'
-- Create test users
INSERT INTO users (id, external_subject, display_name) VALUES
  ('11111111-1111-1111-1111-111111111111', 'user_a', 'User A'),
  ('22222222-2222-2222-2222-222222222222', 'user_b', 'User B');

-- Create test tenants
INSERT INTO tenants (id, legal_name, trade_name) VALUES
  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'Company A', 'A Corp'),
  ('bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb', 'Company B', 'B Corp');

-- Get tenant_admin role
INSERT INTO memberships (tenant_id, user_id, role_id, status)
SELECT t.id, u.id, r.id, 'active'
FROM tenants t, users u, roles r
WHERE (t.id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa' AND u.id = '11111111-1111-1111-1111-111111111111')
  AND r.key = 'tenant_admin' AND r.tenant_id IS NULL;

INSERT INTO memberships (tenant_id, user_id, role_id, status)
SELECT t.id, u.id, r.id, 'active'
FROM tenants t, users u, roles r
WHERE (t.id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb' AND u.id = '22222222-2222-2222-2222-222222222222')
  AND r.key = 'tenant_admin' AND r.tenant_id IS NULL;
SQL

echo -e "${GREEN}✓ Test data created${RESET}\n"

# Test 1: User A can see Tenant A
echo -n "Test 1: User A accesses Tenant A ... "
result=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SET app.current_user_id = '11111111-1111-1111-1111-111111111111';
  SET app.is_system_admin = FALSE;
  SELECT COUNT(*) FROM tenants WHERE id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
")
if [ "$result" -eq 1 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 1, got $result)"
  exit 1
fi

# Test 2: User A cannot see Tenant B (isolation)
echo -n "Test 2: User A blocked from Tenant B ... "
result=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SET app.current_user_id = '11111111-1111-1111-1111-111111111111';
  SET app.is_system_admin = FALSE;
  SELECT COUNT(*) FROM tenants WHERE id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
")
if [ "$result" -eq 0 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 0, got $result)"
  exit 1
fi

# Test 3: User B can see Tenant B
echo -n "Test 3: User B accesses Tenant B ... "
result=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SET app.current_user_id = '22222222-2222-2222-2222-222222222222';
  SET app.is_system_admin = FALSE;
  SELECT COUNT(*) FROM tenants WHERE id = 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb';
")
if [ "$result" -eq 1 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 1, got $result)"
  exit 1
fi

# Test 4: User B cannot see Tenant A
echo -n "Test 4: User B blocked from Tenant A ... "
result=$(docker exec omnira-postgres psql -U omnira -d omnira_dev -t -c "
  SET app.current_user_id = '22222222-2222-2222-2222-222222222222';
  SET app.is_system_admin = FALSE;
  SELECT COUNT(*) FROM tenants WHERE id = 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa';
")
if [ "$result" -eq 0 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 0, got $result)"
  exit 1
fi

# Cleanup
echo -e "\n${BOLD}Cleanup${RESET}"
docker exec omnira-postgres psql -U omnira -d omnira_dev << 'SQL' > /dev/null 2>&1
DELETE FROM memberships WHERE user_id IN ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222');
DELETE FROM users WHERE id IN ('11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222');
DELETE FROM tenants WHERE id IN ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb');
SQL

echo -e "${GREEN}✓ Test data cleaned up${RESET}\n"
echo -e "${GREEN}${BOLD}All isolation tests passed ✓${RESET}"
