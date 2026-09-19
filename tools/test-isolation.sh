#!/bin/bash
# test-isolation.sh — basic isolation test (A vs B tenant)
#
# IMPORTANTE: este teste conecta como a role de aplicação real
# (omnira_app), NUNCA como a role admin/migração (omnira). Antes desta
# correção o script usava `-U omnira`, que é superuser e sempre contorna
# RLS — o teste "passava" independentemente de a RLS estar de fato
# funcionando, um falso positivo silencioso. Ver
# docs/research/deskcomm/REUSE-AUDIT.md para o achado equivalente no
# projeto doador (DeskcommCRM) que motivou esta revisão.
#
# IDs são gerados dinamicamente (uuid_generate_v4()) em vez de fixos: um
# UUID fixo hardcoded pode colidir com dados reais de outro seed/sessão de
# desenvolvimento (aconteceu durante o desenvolvimento deste script).

set -e

APP_DB_USER="omnira_app"
BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

echo -e "${BOLD}Isolation Test (A vs B Tenant) — via role de aplicacao (${APP_DB_USER})${RESET}\n"

# Setup precisa de privilegios elevados (bypassa RLS de proposito, é dado
# de teste sendo semeado, não uma leitura/escrita da aplicação real).
SETUP_OUT=$(docker exec -i omnira-postgres psql -U omnira -d omnira_dev -tA << 'SQL'
WITH ua AS (
  INSERT INTO users (id, external_subject, email, status)
  VALUES (gen_random_uuid(), 'test-isolation-user-a', 'test-isolation-user-a@example.invalid', 'active')
  RETURNING id
), ub AS (
  INSERT INTO users (id, external_subject, email, status)
  VALUES (gen_random_uuid(), 'test-isolation-user-b', 'test-isolation-user-b@example.invalid', 'active')
  RETURNING id
), ta AS (
  INSERT INTO tenants (id, legal_name, trade_name, isolation_profile, status, created_at, updated_at)
  VALUES (gen_random_uuid(), 'Test Isolation Company A', 'A Corp', 'shared_strong_isolation', 'active', now(), now())
  RETURNING id
), tb AS (
  INSERT INTO tenants (id, legal_name, trade_name, isolation_profile, status, created_at, updated_at)
  VALUES (gen_random_uuid(), 'Test Isolation Company B', 'B Corp', 'shared_strong_isolation', 'active', now(), now())
  RETURNING id
), role AS (
  SELECT id FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL LIMIT 1
), ma AS (
  INSERT INTO memberships (id, tenant_id, user_id, role_id, status, created_at, updated_at)
  SELECT gen_random_uuid(), ta.id, ua.id, role.id, 'active', now(), now() FROM ta, ua, role
), mb AS (
  INSERT INTO memberships (id, tenant_id, user_id, role_id, status, created_at, updated_at)
  SELECT gen_random_uuid(), tb.id, ub.id, role.id, 'active', now(), now() FROM tb, ub, role
)
SELECT ua.id, ub.id, ta.id, tb.id FROM ua, ub, ta, tb;
SQL
)

USER_A=$(echo "$SETUP_OUT" | awk -F'|' '{print $1}')
USER_B=$(echo "$SETUP_OUT" | awk -F'|' '{print $2}')
TENANT_A=$(echo "$SETUP_OUT" | awk -F'|' '{print $3}')
TENANT_B=$(echo "$SETUP_OUT" | awk -F'|' '{print $4}')

if [ -z "$USER_A" ] || [ -z "$TENANT_A" ]; then
  echo -e "${RED}✗ Falha ao criar dados de teste (saida do setup vazia)${RESET}"
  exit 1
fi

echo -e "${GREEN}✓ Test data created${RESET} (tenant_a=$TENANT_A tenant_b=$TENANT_B)\n"

cleanup() {
  docker exec -i omnira-postgres psql -U omnira -d omnira_dev -tA > /dev/null 2>&1 << SQL
DELETE FROM memberships WHERE user_id IN ('$USER_A', '$USER_B');
DELETE FROM users WHERE id IN ('$USER_A', '$USER_B');
DELETE FROM tenants WHERE id IN ('$TENANT_A', '$TENANT_B');
SQL
}
trap cleanup EXIT

run_as_app() {
  local user_id="$1"
  local query="$2"
  docker exec omnira-postgres psql -U "$APP_DB_USER" -d omnira_dev -tA -c "
    BEGIN;
    SELECT set_config('app.current_user_id', '${user_id}', true);
    ${query}
    COMMIT;
  " | grep -E '^[0-9]+$' | tail -1
}

# Test 1: User A can see Tenant A
echo -n "Test 1: User A accesses Tenant A ... "
result=$(run_as_app "$USER_A" "SELECT COUNT(*) FROM tenants WHERE id = '$TENANT_A';")
if [ "$result" -eq 1 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 1, got $result)"
  exit 1
fi

# Test 2: User A cannot see Tenant B (isolation)
echo -n "Test 2: User A blocked from Tenant B ... "
result=$(run_as_app "$USER_A" "SELECT COUNT(*) FROM tenants WHERE id = '$TENANT_B';")
if [ "$result" -eq 0 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 0, got $result)"
  exit 1
fi

# Test 3: User B can see Tenant B
echo -n "Test 3: User B accesses Tenant B ... "
result=$(run_as_app "$USER_B" "SELECT COUNT(*) FROM tenants WHERE id = '$TENANT_B';")
if [ "$result" -eq 1 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 1, got $result)"
  exit 1
fi

# Test 4: User B cannot see Tenant A
echo -n "Test 4: User B blocked from Tenant A ... "
result=$(run_as_app "$USER_B" "SELECT COUNT(*) FROM tenants WHERE id = '$TENANT_A';")
if [ "$result" -eq 0 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 0, got $result)"
  exit 1
fi

# Test 5: sem app.current_user_id setado, ninguem ve nada (fail-closed)
echo -n "Test 5: sem sessao de usuario, acesso negado por padrao ... "
result=$(docker exec omnira-postgres psql -U "$APP_DB_USER" -d omnira_dev -t -c "SELECT COUNT(*) FROM tenants WHERE id IN ('$TENANT_A', '$TENANT_B');" | tr -d '[:space:]')
if [ "$result" -eq 0 ]; then
  echo -e "${GREEN}✓${RESET}"
else
  echo -e "${RED}✗${RESET} (expected 0, got $result — RLS pode estar inerte)"
  exit 1
fi

echo -e "\n${GREEN}${BOLD}All isolation tests passed ✓${RESET}"
