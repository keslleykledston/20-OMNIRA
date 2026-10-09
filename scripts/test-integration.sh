#!/usr/bin/env bash
# test-integration.sh — TEST.HYGIENE.2: run every real-Postgres integration
# package against a disposable Postgres cluster that is physically separate
# from omnira-postgres/omnira_dev.
#
# Why: t.Cleanup-based row deletion is not a crash-recovery boundary — a
# killed/timed-out `go test` process never reaches it, and shared omnira_dev
# accumulates orphaned tenants/users as a result (see docs from
# TEST.DATA.CLEANUP.1E). This script makes the boundary structural instead:
#
#   1. one disposable `postgres:16-alpine` container per invocation, bound
#      only to 127.0.0.1, labeled so a crashed run's leftovers are always
#      identifiable and never confused with anything else on the host;
#   2. one fully-migrated template database inside it, built with the
#      canonical migration tool (tools/apply-migrations.sh) — never a second
#      schema definition;
#   3. one CREATE DATABASE ... TEMPLATE clone per Go package, so packages
#      that have historically collided over shared Postgres state
#      (internal/outbox/adapters, internal/worker/publisher, internal/e2e)
#      get physically separate catalogs;
#   4. every `go test` invocation carries OMNIRA_INTEGRATION_TEST=1 and a
#      database name that must start with omnira_test_ — the same guard
#      (internal/testhelpers.RequireIntegrationDatabase) that every real-
#      Postgres test now goes through refuses anything else, including a
#      direct `go test` run that happens to inherit OMNIRA_DATABASE_URL
#      pointed at omnira_dev.
#
# TEST.HYGIENE.3 extends the same model to NATS/JetStream: ONE disposable
# `nats:2.10-alpine -js` per invocation (same run, shared by every package —
# unlike Postgres, no test here mutates cluster-global NATS state, so
# per-package physical separation buys nothing; unique stream/subject/durable
# names per test already isolate packages from each other within it), given a
# server name derived from RUN_ID. Every real-NATS test now goes through
# internal/testhelpers.RequireIntegrationNATS, which refuses to run against
# the known dev/pilot addresses (127.0.0.1:4222, localhost:4222, nats:4222),
# refuses any non-loopback destination, and — since a denylist of known
# addresses is never complete — attests via a read-only connection that the
# server on the other end actually identifies itself as this run's disposable
# server before any test may create a stream/consumer/publish a message.
#
# If this script (or the whole host) is killed mid-run, the disposable
# container is the only thing that can contain leftover fixtures; omnira_dev
# is never reachable from it. A leftover container carries this run's EXACT
# RUN_ID as a second label, so it is removable without any risk of touching a
# different, still-active run:
#   scripts/test-integration.sh --cleanup-run <RUN_ID>
#
# TEST.HYGIENE.2F: an earlier version of this script had a broad
# `--cleanup-stale [minutes]` mode that matched every container carrying only
# com.omnira.integration-test=true and removed any of them whose age crossed
# a threshold — with threshold 0 (as used for the crash-proof evidence) that
# matched and removed EVERY labeled container, including a different,
# legitimately still-running invocation. That mode is removed rather than
# patched: a safe age-based garbage collector needs more thought than this
# narrow patch warrants (see --list-stale below for the read-only variant,
# and the P1/P2 list in the human gate for that follow-up).
#
# Usage:
#   scripts/test-integration.sh                 # run every integration package
#   scripts/test-integration.sh <pkg> [<pkg>...] # run only the given packages
#                                                   (e.g. ./internal/outbox/adapters)
#   scripts/test-integration.sh --cleanup-run <RUN_ID>  # remove exactly one
#                                                          run's container(s),
#                                                          matched on BOTH
#                                                          labels — never a
#                                                          prefix/substring
#   scripts/test-integration.sh --list-stale [minutes]  # read-only: list
#                                                          labeled containers
#                                                          and their age;
#                                                          removes nothing
set -euo pipefail
cd "$(dirname "$0")/.."

LABEL_KEY="com.omnira.integration-test"

# Exact-match cleanup: selects only container(s) carrying BOTH
# com.omnira.integration-test=true AND com.omnira.integration-test.run=<id>,
# via two separate --filter label= clauses (Docker ANDs them), which is a
# server-side exact key=value match — never a prefix or substring. A run ID
# that matches nothing exits cleanly instead of erroring.
cleanup_run() {
  local run_id="$1"
  if [ -z "$run_id" ]; then
    echo "usage: $0 --cleanup-run <RUN_ID>" >&2
    exit 2
  fi
  echo "== looking for containers with ${LABEL_KEY}=true AND ${LABEL_KEY}.run=${run_id}"
  local ids
  ids=$(docker ps -aq --filter "label=${LABEL_KEY}=true" --filter "label=${LABEL_KEY}.run=${run_id}")
  if [ -z "$ids" ]; then
    echo "== no container matches run ${run_id} (already cleaned up, or it never existed) — nothing to do"
    exit 0
  fi
  local n=0
  while read -r cid; do
    [ -z "$cid" ] && continue
    name=$(docker inspect -f '{{.Name}}' "$cid" | sed 's#^/##')
    echo "   removing ${name} (exact match on run ${run_id})"
    docker rm -fv "$cid" >/dev/null 2>&1 || true
    n=$((n+1))
  done <<< "$ids"
  echo "== removed ${n} container(s) belonging to run ${run_id}"
}

list_stale() {
  local max_age_min="${1:-120}"
  echo "== ${LABEL_KEY}=true containers (read-only; nothing is removed)"
  local now cid started age_min name run
  now=$(date +%s)
  local total=0
  while read -r cid; do
    [ -z "$cid" ] && continue
    started=$(docker inspect -f '{{.State.StartedAt}}' "$cid")
    started_epoch=$(date -d "$started" +%s 2>/dev/null || echo "$now")
    age_min=$(( (now - started_epoch) / 60 ))
    name=$(docker inspect -f '{{.Name}}' "$cid" | sed 's#^/##')
    run=$(docker inspect -f '{{index .Config.Labels "'"${LABEL_KEY}"'.run"}}' "$cid")
    flag="active"
    [ "$age_min" -ge "$max_age_min" ] && flag="STALE (>= ${max_age_min}m) — clean up with: $0 --cleanup-run ${run}"
    echo "   ${name}  run=${run}  age=${age_min}m  ${flag}"
    total=$((total+1))
  done < <(docker ps -aq --filter "label=${LABEL_KEY}=true")
  echo "== ${total} labeled container(s) total"
}

case "${1:-}" in
  --cleanup-run)
    cleanup_run "${2:-}"
    exit 0
    ;;
  --list-stale)
    list_stale "${2:-120}"
    exit 0
    ;;
esac

RUN_ID="$(date -u +%Y%m%dT%H%M%SZ)-$$"
RUNSHORT=$(printf '%s' "$RUN_ID" | md5sum | cut -c1-8)
CONTAINER="omnira-test-pg-${RUNSHORT}"
NATS_CONTAINER="omnira-test-nats-${RUNSHORT}"
NATS_SERVER_NAME="omnira-test-${RUNSHORT}"
PG_USER=omnira
PG_PASSWORD=$(head -c32 /dev/urandom | base64 | tr -dc 'a-zA-Z0-9' | head -c24)
TEMPLATE_DB=omnira_test_template

echo "== RUN_ID=${RUN_ID}"

# Registered before either container is started, so a failure partway through
# provisioning (e.g. Postgres starts but NATS fails) still tears down
# whatever exists for this exact RUN_ID — `docker rm -f` on a name that was
# never created is a harmless no-op.
cleanup() {
  local status=$?
  echo "== tearing down ${CONTAINER} / ${NATS_CONTAINER}"
  docker rm -fv "${CONTAINER}" >/dev/null 2>&1 || true
  docker rm -fv "${NATS_CONTAINER}" >/dev/null 2>&1 || true
  exit $status
}
trap cleanup EXIT

echo "== starting disposable Postgres cluster (${CONTAINER}, 127.0.0.1 only, test-local credentials)"
docker run -d --name "${CONTAINER}" \
  --label "${LABEL_KEY}=true" \
  --label "${LABEL_KEY}.run=${RUN_ID}" \
  -e POSTGRES_USER="${PG_USER}" -e POSTGRES_PASSWORD="${PG_PASSWORD}" \
  -p 127.0.0.1::5432 \
  postgres:16-alpine >/dev/null

PORT=$(docker port "${CONTAINER}" 5432/tcp | head -1 | cut -d: -f2)
echo "== ${CONTAINER} -> 127.0.0.1:${PORT}"

for i in $(seq 1 30); do
  docker exec "${CONTAINER}" pg_isready -U "${PG_USER}" >/dev/null 2>&1 && break
  sleep 0.5
done

echo "== starting disposable NATS/JetStream (${NATS_CONTAINER}, 127.0.0.1 only, server_name=${NATS_SERVER_NAME})"
docker run -d --name "${NATS_CONTAINER}" \
  --label "${LABEL_KEY}=true" \
  --label "${LABEL_KEY}.run=${RUN_ID}" \
  -p 127.0.0.1::4222 -p 127.0.0.1::8222 \
  nats:2.10-alpine -js -m 8222 --server_name "${NATS_SERVER_NAME}" >/dev/null

NATS_PORT=$(docker port "${NATS_CONTAINER}" 4222/tcp | head -1 | cut -d: -f2)
NATS_MONITOR_PORT=$(docker port "${NATS_CONTAINER}" 8222/tcp | head -1 | cut -d: -f2)
NATS_URL="nats://127.0.0.1:${NATS_PORT}"
NATS_MONITOR_URL="http://127.0.0.1:${NATS_MONITOR_PORT}"
echo "== ${NATS_CONTAINER} -> ${NATS_URL} (monitor ${NATS_MONITOR_URL})"

nats_ready=0
for i in $(seq 1 30); do
  if curl -sf "${NATS_MONITOR_URL}/healthz" >/dev/null 2>&1; then
    nats_ready=1
    break
  fi
  sleep 0.3
done
if [ "$nats_ready" != "1" ]; then
  echo "FATAL: disposable NATS did not become ready in time" >&2
  exit 1
fi
reported_name=$(curl -sf "${NATS_MONITOR_URL}/varz" | python3 -c "import json,sys; print(json.load(sys.stdin).get('server_name',''))" 2>/dev/null || echo "")
if [ "$reported_name" != "$NATS_SERVER_NAME" ]; then
  echo "FATAL: disposable NATS reports server_name=${reported_name}, expected ${NATS_SERVER_NAME}" >&2
  exit 1
fi
echo "   server_name confirmed: ${reported_name}"

psql_owner() { PGPASSWORD="${PG_PASSWORD}" psql -h 127.0.0.1 -p "${PORT}" -U "${PG_USER}" -v ON_ERROR_STOP=1 -q "$@"; }

echo "== creating template database (${TEMPLATE_DB})"
psql_owner -d postgres -c "CREATE DATABASE ${TEMPLATE_DB}" >/dev/null

echo "== applying canonical migration chain to the template (tools/apply-migrations.sh)"
# No query string here on purpose: tools/apply-migrations.sh's connection-string
# parser splits on the first "/" for the database name and does not strip a
# trailing "?..."; a disposable local container needs no sslmode parameter
# anyway (psql's default "prefer" already falls back to plaintext).
DATABASE_URL="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PORT}/${TEMPLATE_DB}" \
  bash tools/apply-migrations.sh up

echo "== proving template state before freezing"
# This repo tracks no schema_migrations ledger table (tools/apply-migrations.sh
# just applies every *.up.sql in order) — the highest-numbered file actually
# present and applied IS the schema version.
schema_version=$(ls migrations/*.up.sql | sort | tail -1 | xargs basename)
force_rls_tables=$(psql_owner -d "${TEMPLATE_DB}" -tAc "SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace AND relforcerowsecurity")
app_role=$(psql_owner -d "${TEMPLATE_DB}" -tAc "SELECT rolname||'|super='||rolsuper||'|bypassrls='||rolbypassrls FROM pg_roles WHERE rolname='omnira_app'")
template_tenants=$(psql_owner -d "${TEMPLATE_DB}" -tAc "SELECT count(*) FROM tenants")
echo "   schema_version=${schema_version}"
echo "   tables with FORCE RLS=${force_rls_tables}"
echo "   omnira_app role: ${app_role}"
echo "   template tenant fixture count=${template_tenants} (expected 0 — schema/bootstrap only, no test fixtures)"
if [ -z "${app_role}" ]; then
  echo "FATAL: omnira_app role missing after migrations" >&2
  exit 1
fi
if [ "${template_tenants}" != "0" ]; then
  echo "FATAL: template database is not clean (${template_tenants} tenant rows) — refusing to clone it" >&2
  exit 1
fi

echo "== freezing template for cloning"
psql_owner -d postgres -c "SELECT pg_terminate_backend(pid) FROM pg_stat_activity WHERE datname='${TEMPLATE_DB}' AND pid <> pg_backend_pid()" >/dev/null
psql_owner -d postgres -c "UPDATE pg_database SET datistemplate = true, datallowconn = false WHERE datname = '${TEMPLATE_DB}'" >/dev/null

if [ "$#" -gt 0 ]; then
  PACKAGES=("$@")
else
  # Reliable discovery from source: any package with a test file that goes
  # through EITHER canonical integration guard (Postgres or NATS) IS an
  # integration package. Union, not two separate lists — a package needing
  # only NATS (internal/worker/routing) must not be silently skipped just
  # because it never touches Postgres.
  # internal/testhelpers itself is excluded: its own guard tests call
  # RequireIntegrationDatabase/RequireIntegrationNATS with literal fake
  # connection strings (t.Setenv) purely to unit-test the guard's parsing
  # logic — they never need or want provisioned infrastructure.
  mapfile -t PACKAGES < <(grep -rl "testhelpers.RequireIntegrationDatabase\|testhelpers.RequireIntegrationNATS" --include="*_test.go" internal | grep -v '^internal/testhelpers/' | xargs -n1 dirname | sort -u | sed 's#^#./#')
fi
echo "== ${#PACKAGES[@]} integration package(s) to run"

FAILED=()
for pkg in "${PACKAGES[@]}"; do
  pkghash=$(printf '%s' "${pkg}" | md5sum | cut -c1-8)
  dbname="omnira_test_${RUNSHORT}_${pkghash}"

  psql_owner -d postgres -c "CREATE DATABASE ${dbname} TEMPLATE ${TEMPLATE_DB}" >/dev/null
  psql_owner -d postgres -c "GRANT CONNECT ON DATABASE ${dbname} TO omnira_app" >/dev/null

  OWNER_URL="postgres://${PG_USER}:${PG_PASSWORD}@127.0.0.1:${PORT}/${dbname}?sslmode=disable"
  APP_URL="postgres://omnira_app:omnira_app@127.0.0.1:${PORT}/${dbname}?sslmode=disable"

  echo "== ${pkg} -> ${dbname} / ${NATS_URL}"
  # Every package gets both isolated dependencies; a package that only needs
  # one of them simply never calls the other guard, so the unused env is
  # inert (TEST.HYGIENE.3 §12). All real-NATS test resources this run creates
  # are uniquely named per test (never OMNIRA_PUBLISHER_TEST-style fixed
  # names), so packages sharing this one disposable NATS server stay safe to
  # run concurrently if a future revision parallelizes this loop.
  if docker run --rm --network host \
      -v "$(pwd)":/app -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
      -e OMNIRA_INTEGRATION_TEST=1 \
      -e OMNIRA_INTEGRATION_RUN_ID="${RUN_ID}" \
      -e OMNIRA_DATABASE_URL="${OWNER_URL}" \
      -e OMNIRA_APP_DATABASE_URL="${APP_URL}" \
      -e OMNIRA_NATS_URL="${NATS_URL}" \
      -e OMNIRA_NATS_MONITOR_URL="${NATS_MONITOR_URL}" \
      -e OMNIRA_NATS_SERVER_NAME="${NATS_SERVER_NAME}" \
      golang:1.25 sh -c "git config --global --add safe.directory /app && go test -count=1 ${pkg}"; then
    :
  else
    FAILED+=("${pkg}")
  fi
done

echo
if [ "${#FAILED[@]}" -gt 0 ]; then
  echo "FAILED packages (${#FAILED[@]}):"
  printf '  %s\n' "${FAILED[@]}"
  exit 1
fi
echo "== all ${#PACKAGES[@]} integration package(s) passed"
