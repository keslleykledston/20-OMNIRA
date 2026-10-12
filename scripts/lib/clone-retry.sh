#!/usr/bin/env bash
# The disposable cluster of scripts/test-integration.sh sometimes drops the connection ("server closed the connection unexpectedly") right after a heavy run;
# that is the infrastructure, not the code under test, so cloning the template database is retried a few times before the run is declared failed. Anything that is
# NOT a dropped or refused connection (permission, bad template, name clash) is a real failure and is reported as it is, on the first attempt.
#
# Needs from the caller: psql_owner (a function running psql as the owner), CONTAINER, PG_USER. CLONE_ATTEMPTS (5) and CLONE_WAIT_SECONDS (30) tune it.
#   clone_template_with_retry <new database> <template database> <label for messages>     returns 0 when cloned, 1 otherwise

CLONE_DROPPED_RE="server closed the connection|connection refused|could not connect|terminating connection|the database system is (starting|shutting)|connection to server .* failed"

clone_template_with_retry() {
  local dbname=$1 template=$2 label=$3 attempts=${CLONE_ATTEMPTS:-5} attempt clone_err
  for attempt in $(seq 1 "${attempts}"); do
    if clone_err=$(psql_owner -d postgres -c "CREATE DATABASE ${dbname} TEMPLATE ${template}" 2>&1 >/dev/null); then return 0; fi
    if ! printf '%s' "${clone_err}" | grep -qiE "${CLONE_DROPPED_RE}"; then
      echo "FATAL: cloning the template for ${label} failed: ${clone_err}" >&2
      return 1
    fi
    echo "   (clone of ${dbname} failed with a dropped connection, attempt ${attempt}/${attempts}: ${clone_err}; waiting for the cluster)" >&2
    psql_owner -d postgres -c "DROP DATABASE IF EXISTS ${dbname}" >/dev/null 2>&1 || true
    local i
    for i in $(seq 1 "${CLONE_WAIT_SECONDS:-30}"); do docker exec "${CONTAINER}" pg_isready -U "${PG_USER}" >/dev/null 2>&1 && break; sleep 1; done
  done
  return 1
}
