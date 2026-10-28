#!/usr/bin/env bash
# test-upgrade-n1.sh — F4: upgrade N-1 -> N against a real, disposable Postgres.
#
# N-1 checkpoint: migration 000168 (hub_delegated_attendance_events) — the last migration that
# actually ran in production before this release-engineering program's own ADR-0050..0053 slice
# (000169-000172) and before release/components.yaml declared platform_release's migration scope.
# A real prior checkpoint, not an invented version number: this host ran exactly this schema
# recently (see docs/release/IMPLEMENTATION-STATUS.md F0/F3).
#
# Proves (docs/release/spec/..._PACOTE_EXECUCAO.md F4 + §13 acceptance matrix):
#   1. data written while the schema was at N-1 survives the upgrade to N unchanged;
#   2. every migration after the checkpoint (000169-HEAD) is expand_only-clean per
#      release/policies/compatibility.yaml (lint-migration-safety.sh) — the actual compatibility
#      promise this host currently makes (minimum_upgrade_window: 1), not just an assumption;
#   3. the ledger ends at the real newest migration, with a checksum for everything this run
#      applied;
#   4. RLS/FORCE RLS on tables the checkpoint-era data lives in survived the upgrade.
#
# Usage: scripts/release/test-upgrade-n1.sh [checkpoint_version]
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

CHECKPOINT="${1:-000168}"
LABEL=com.omnira.integration-test
RUN="$(date -u +%Y%m%dT%H%M%SZ)-$$"
C="omnira-test-upgrade-n1-$$"
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'; FAILS=0
ok()  { echo "  ${GRN}✓${NC} $*"; }
bad() { echo "  ${RED}✗ $*${NC}"; FAILS=$((FAILS+1)); }

WORK=$(mktemp -d)
cleanup() { docker rm -fv "$C" >/dev/null 2>&1; rm -rf "$WORK"; }
trap cleanup EXIT

checkpoint_files() {
  for f in migrations/*.up.sql; do
    v=$(basename "$f" .up.sql)
    [ "${v%%_*}" \< "$CHECKPOINT" -o "${v%%_*}" = "$CHECKPOINT" ] && echo "$f"
  done
}
newer_than_checkpoint() {
  for f in migrations/*.up.sql; do
    v=$(basename "$f" .up.sql)
    [ "${v%%_*}" \> "$CHECKPOINT" ] && echo "$f"
  done
}

n1_files=$(checkpoint_files)
n1_count=$(echo "$n1_files" | grep -c . || true)
upgrade_files=$(newer_than_checkpoint)
upgrade_count=$(echo "$upgrade_files" | grep -c . || true)
echo "== N-1 checkpoint: ${CHECKPOINT} (${n1_count} migration(s) at/before it, ${upgrade_count} to apply going to N)"
[ "$upgrade_count" -gt 0 ] || { echo "no migrations after ${CHECKPOINT} — checkpoint is not actually N-1 of anything, pick an older one" >&2; exit 2; }

echo "== starting disposable Postgres ($C)"
docker run -d --name "$C" --label "$LABEL=true" --label "$LABEL.run=$RUN" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=x postgres:16-alpine >/dev/null || { bad "could not start container"; exit 1; }
for i in $(seq 1 60); do
  [ "$(docker logs "$C" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 0.5
done
docker exec "$C" psql -U omnira -d postgres -c "CREATE DATABASE n1test" >/dev/null
db() { docker exec -i "$C" psql -U omnira -d n1test -X -q -tA -v ON_ERROR_STOP=1 "$@"; }

echo "== 1. build the N-1 state (${n1_count} migrations)"
mkdir -p "$WORK/n1"
# shellcheck disable=SC2086
cp $n1_files "$WORK/n1/"
docker cp "$WORK/n1" "$C":/tmp/migrations-n1 >/dev/null
docker cp tools/migrate-sql.sh "$C":/tmp/migrate-sql.sh >/dev/null
out=$(docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/n1test" -e MIGRATIONS_DIR=/tmp/migrations-n1 "$C" sh /tmp/migrate-sql.sh up 2>&1) \
  || { bad "N-1 migration chain failed: $out"; exit 1; }
ledger_n1=$(db -c "SELECT max(version) FROM schema_migrations")
[ "${ledger_n1%%_*}" = "$CHECKPOINT" ] && ok "N-1 state reached: ledger at ${ledger_n1}" || bad "N-1 ledger is ${ledger_n1}, expected ${CHECKPOINT}_*"

echo "== 2. write data as the N-1-era application would have"
db <<'SQL' >/dev/null
INSERT INTO tenants(id,legal_name,status) VALUES ('99999999-9999-9999-9999-999999999999','N-1 Tenant','active');
INSERT INTO channel_connections(id,tenant_id,channel,provider,provider_kind,external_number_id,status,capabilities)
  VALUES ('88888888-8888-8888-8888-888888888888','99999999-9999-9999-9999-999999999999','whatsapp','waha','unofficial','n1-number','active','{}');
INSERT INTO contacts(id,tenant_id,display_name,phone_e164) VALUES ('77777777-7777-7777-7777-777777777777','99999999-9999-9999-9999-999999999999','N-1 Contact','+5511999990000');
INSERT INTO conversations(id,tenant_id,channel_connection_id,contact_id,status)
  VALUES ('66666666-6666-6666-6666-666666666666','99999999-9999-9999-9999-999999999999','88888888-8888-8888-8888-888888888888','77777777-7777-7777-7777-777777777777','open');
SQL
rowcheck1=$(db -c "SELECT count(*) FROM conversations WHERE id='66666666-6666-6666-6666-666666666666'")
[ "$rowcheck1" = 1 ] && ok "N-1-era fixture row written" || { bad "fixture write failed"; exit 1; }
force_rls_before=$(db -c "SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace AND relforcerowsecurity AND relname IN ('tenants','conversations','contacts')")

echo "== 3. upgrade to N: point at the FULL migration set, apply the rest"
docker cp migrations "$C":/tmp/migrations-full >/dev/null
out=$(docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/n1test" -e MIGRATIONS_DIR=/tmp/migrations-full "$C" sh /tmp/migrate-sql.sh up 2>&1) \
  || { bad "upgrade to N failed: $out"; exit 1; }
applied_now=$(echo "$out" | grep -oE '[0-9]+ applied now' | grep -oE '^[0-9]+')
[ "$applied_now" = "$upgrade_count" ] && ok "upgrade applied exactly the ${upgrade_count} migration(s) after the checkpoint" || bad "upgrade applied ${applied_now}, expected ${upgrade_count}"

echo "== 4. the N-1-era data survived the upgrade, unchanged"
rowcheck2=$(db -c "SELECT count(*) FROM conversations WHERE id='66666666-6666-6666-6666-666666666666'")
[ "$rowcheck2" = 1 ] && ok "N-1-era fixture row still present after upgrade" || bad "N-1-era fixture row lost or duplicated ($rowcheck2)"
force_rls_after=$(db -c "SELECT count(*) FROM pg_class WHERE relkind='r' AND relnamespace='public'::regnamespace AND relforcerowsecurity AND relname IN ('tenants','conversations','contacts')")
[ "$force_rls_after" = "$force_rls_before" ] && [ "$force_rls_after" = 3 ] && ok "FORCE RLS on tenants/conversations/contacts unchanged by the upgrade (${force_rls_after}/3)" || bad "FORCE RLS flags changed: before=${force_rls_before} after=${force_rls_after}"

echo "== 5. ledger ends at the real newest migration"
newest=$(ls migrations/*.up.sql | sort | tail -1 | xargs basename | sed 's/\.up\.sql$//')
ledger_n=$(db -c "SELECT max(version) FROM schema_migrations")
[ "$ledger_n" = "$newest" ] && ok "ledger at HEAD: ${ledger_n}" || bad "ledger is ${ledger_n}, expected ${newest}"
docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/n1test" -e MIGRATIONS_DIR=/tmp/migrations-full "$C" sh /tmp/migrate-sql.sh verify >"$WORK/verify.out" 2>&1
grep -q "verify: OK" "$WORK/verify.out" && ok "checksum ledger clean end to end" || { bad "verify should be clean"; cat "$WORK/verify.out"; }

echo "== 6. every migration after the checkpoint keeps the expand_only promise (compatibility.yaml: minimum_upgrade_window=1)"
lint_fail=0
for f in $upgrade_files; do
  bash scripts/release/lint-migration-safety.sh "$f" expand_only >/dev/null 2>"$WORK/lint-$$-$(basename "$f").err" || { lint_fail=$((lint_fail+1)); echo "    contract-shaped: $f"; cat "$WORK/lint-$$-$(basename "$f").err"; }
done
[ "$lint_fail" -eq 0 ] && ok "all ${upgrade_count} post-checkpoint migrations are expand_only-clean" || bad "${lint_fail} post-checkpoint migration(s) are contract-shaped (would break an N-1 reader)"

echo "== result"
if [ "$FAILS" -eq 0 ]; then echo "${GRN}UPGRADE N-1 -> N VALIDATED${NC} (checkpoint ${CHECKPOINT}, ${upgrade_count} migration(s) forward)"; else echo "${RED}$FAILS check(s) failed${NC}"; fi
exit "$FAILS"
