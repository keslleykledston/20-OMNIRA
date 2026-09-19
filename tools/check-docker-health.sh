#!/bin/bash
# check-docker-health.sh — valida saúde dos serviços docker-compose

set -e

BOLD="\033[1m"
GREEN="\033[32m"
RED="\033[31m"
RESET="\033[0m"

check_postgres() {
  echo -n "Checking Postgres... "
  if docker exec omnira-postgres pg_isready -U omnira -d omnira_dev > /dev/null 2>&1; then
    echo -e "${GREEN}✓${RESET}"
    return 0
  else
    echo -e "${RED}✗${RESET}"
    return 1
  fi
}

check_nats() {
  echo -n "Checking NATS... "
  if nc -z localhost 4222 > /dev/null 2>&1; then
    echo -e "${GREEN}✓${RESET}"
    return 0
  else
    echo -e "${RED}✗${RESET}"
    return 1
  fi
}

check_otel() {
  echo -n "Checking OTel Collector... "
  if curl -sf http://localhost:13133/healthz > /dev/null 2>&1; then
    echo -e "${GREEN}✓${RESET}"
    return 0
  else
    echo -e "${RED}✗${RESET}"
    return 1
  fi
}

check_network() {
  echo -n "Checking network connectivity... "
  if docker network ls | grep -q omnira-network; then
    echo -e "${GREEN}✓${RESET}"
    return 0
  else
    echo -e "${RED}✗${RESET}"
    return 1
  fi
}

main() {
  echo -e "${BOLD}OMNIRA Infrastructure Health Check${RESET}"
  echo ""

  failed=0
  check_network || ((failed++))
  check_postgres || ((failed++))
  check_nats || ((failed++))
  check_otel || ((failed++))

  echo ""
  if [ $failed -eq 0 ]; then
    echo -e "${GREEN}${BOLD}All services healthy ✓${RESET}"
    exit 0
  else
    echo -e "${RED}${BOLD}$failed service(s) unhealthy ✗${RESET}"
    exit 1
  fi
}

main "$@"
