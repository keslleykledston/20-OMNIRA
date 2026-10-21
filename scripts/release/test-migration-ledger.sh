#!/usr/bin/env bash
# test-migration-ledger.sh — F4: proves tools/migrate-sql.sh's checksum ledger against a real,
# disposable Postgres (same isolation model as scripts/test-integration.sh: labeled container,
# bound to 127.0.0.1 only, removed on exit). Exercises the acceptance-matrix rows from
# docs/release/spec/..._PACOTE_EXECUCAO.md §13:
#   "Migrations rodam duas vezes por retry -> uma aplicação válida no ledger, idempotente"
#   "Modificação em *.up.sql já aplicada -> mismatch de checksum detectado e instalação bloqueada"
# plus the UNKNOWN (pre-ledger row) and opt-in backfill-checksums paths.
#
# Usage: scripts/release/test-migration-ledger.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

LABEL=com.omnira.integration-test
RUN="$(date -u +%Y%m%dT%H%M%SZ)-$$"
C="omnira-test-mig-ledger-$$"
GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'; FAILS=0
ok()  { echo "  ${GRN}✓${NC} $*"; }
bad() { echo "  ${RED}✗ $*${NC}"; FAILS=$((FAILS+1)); }

WORK=$(mktemp -d)
cleanup() { docker rm -fv "$C" >/dev/null 2>&1; rm -rf "$WORK"; }
trap cleanup EXIT

echo "== starting disposable Postgres ($C)"
docker run -d --name "$C" --label "$LABEL=true" --label "$LABEL.run=$RUN" \
  -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=x postgres:16-alpine >/dev/null || { bad "could not start container"; exit 1; }
for i in $(seq 1 60); do
  [ "$(docker logs "$C" 2>&1 | grep -c 'ready to accept connections')" -ge 2 ] && break
  sleep 0.5
done

db() { docker exec "$C" psql -U omnira -d ledgertest -X -q -tA -v ON_ERROR_STOP=1 "$@"; }
run_migrate() { # run_migrate <migrations-dir-in-container> <extra-env...>
  local dir="$1"; shift
  docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/ledgertest" -e MIGRATIONS_DIR="$dir" "$@" "$C" sh /tmp/migrate-sql.sh "${CMD:-up}"
}

docker exec "$C" psql -U omnira -d postgres -c "CREATE DATABASE ledgertest" >/dev/null
docker cp tools/migrate-sql.sh "$C":/tmp/migrate-sql.sh >/dev/null
docker cp migrations "$C":/tmp/migrations >/dev/null

echo "== 1. first apply"
out1=$(CMD=up run_migrate /tmp/migrations 2>&1) || { bad "first apply failed: $out1"; exit 1; }
n1=$(db -c "SELECT count(*) FROM schema_migrations")
[ "$n1" -gt 0 ] && ok "first apply: $n1 migration(s) recorded" || bad "first apply recorded 0 rows"
unset_count=$(db -c "SELECT count(*) FROM schema_migrations WHERE checksum IS NULL")
[ "$unset_count" = 0 ] && ok "every newly-applied row got a checksum" || bad "$unset_count row(s) applied without a checksum"

echo "== 2. second apply (idempotent retry — acceptance matrix row)"
out2=$(CMD=up run_migrate /tmp/migrations 2>&1) || { bad "second apply failed: $out2"; }
echo "$out2" | grep -q "0 applied now" && ok "retry applies 0 new migrations" || bad "retry did not report 0 applied: $out2"
n2=$(db -c "SELECT count(*) FROM schema_migrations")
[ "$n2" = "$n1" ] && ok "ledger row count unchanged by retry ($n2)" || bad "ledger row count changed: $n1 -> $n2"

echo "== 3. verify is clean"
CMD=verify run_migrate /tmp/migrations >"$WORK/verify1.out" 2>&1
[ $? -eq 0 ] && grep -q "verify: OK" "$WORK/verify1.out" && ok "verify: clean ledger" || { bad "verify should be clean"; cat "$WORK/verify1.out"; }

echo "== 4. modify an already-applied migration file on disk (acceptance matrix row)"
newest=$(ls migrations/*.up.sql | sort | tail -1 | xargs basename)
docker exec "$C" sh -c "echo '-- tampered by test-migration-ledger.sh' >> /tmp/migrations/$newest"
CMD=verify run_migrate /tmp/migrations >"$WORK/verify2.out" 2>&1
rc=$?
if [ "$rc" -ne 0 ] && grep -q "MODIFIED ${newest%.up.sql}" "$WORK/verify2.out"; then
  ok "verify detects the modified file and fails closed (exit $rc)"
else
  bad "verify did not detect the modification (exit $rc)"; cat "$WORK/verify2.out"
fi

echo "== 4b. the SAME tamper must also block normal 'up' (Codex round 2: not just a separate verify)"
docker exec "$C" sh -c "echo '-- a brand new pending migration, never applied' > /tmp/migrations/999999_never_applied.up.sql"
out_up_tampered=$(CMD=up run_migrate /tmp/migrations 2>&1)
rc=$?
if [ "$rc" -ne 0 ] && echo "$out_up_tampered" | grep -q "REFUSING" && echo "$out_up_tampered" | grep -q "MODIFIED ${newest%.up.sql}"; then
  ok "up refuses to apply ANYTHING (including the new pending migration) while a prior migration is tampered (exit $rc)"
else
  bad "up should have refused on the tampered file (exit $rc)"; echo "$out_up_tampered"
fi
pending_still=$(db -c "SELECT count(*) FROM schema_migrations WHERE version='999999_never_applied'")
[ "$pending_still" = 0 ] && ok "the new pending migration was NOT applied while drift was present" || bad "the new pending migration was applied despite drift ($pending_still)"
docker exec "$C" rm -f /tmp/migrations/999999_never_applied.up.sql

# restore it so the rest of this test isn't poisoned by the tamper
docker cp "migrations/$newest" "$C":/tmp/migrations/"$newest" >/dev/null
out_up_clean=$(CMD=up run_migrate /tmp/migrations 2>&1) || { bad "up should succeed again once the tamper is reverted: $out_up_clean"; }

echo "== 5. delete an already-applied migration file on disk"
victim=$(ls migrations/*.up.sql | sort | sed -n '5p' | xargs basename)
docker exec "$C" rm "/tmp/migrations/$victim"
CMD=verify run_migrate /tmp/migrations >"$WORK/verify3.out" 2>&1
rc=$?
if [ "$rc" -ne 0 ] && grep -q "MISSING  ${victim%.up.sql}" "$WORK/verify3.out"; then
  ok "verify detects the missing file and fails closed (exit $rc)"
else
  bad "verify did not detect the missing file (exit $rc)"; cat "$WORK/verify3.out"
fi
docker cp "migrations/$victim" "$C":/tmp/migrations/"$victim" >/dev/null

echo "== 6. a pre-existing row with no checksum (ledger from before this feature) is UNKNOWN, not a failure"
oldest=$(ls migrations/*.up.sql | sort | head -1 | xargs basename | sed 's/\.up\.sql$//')
db -c "UPDATE schema_migrations SET checksum = NULL WHERE version = '$oldest'" >/dev/null
CMD=verify run_migrate /tmp/migrations >"$WORK/verify4.out" 2>&1
rc=$?
if [ "$rc" -eq 0 ] && grep -q "UNKNOWN  $oldest" "$WORK/verify4.out"; then
  ok "a row with no checksum reports UNKNOWN and does not fail verify"
else
  bad "UNKNOWN row handling is wrong (exit $rc)"; cat "$WORK/verify4.out"
fi

echo "== 7. backfill-checksums refuses without confirmation, then records one with CONFIRM_BACKFILL=1"
docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/ledgertest" -e MIGRATIONS_DIR=/tmp/migrations "$C" sh /tmp/migrate-sql.sh backfill-checksums >/dev/null 2>"$WORK/backfill_refuse.out"
rc=$?
[ "$rc" = 2 ] && grep -q "refusing without CONFIRM_BACKFILL=1" "$WORK/backfill_refuse.out" && ok "backfill-checksums refuses without confirmation" || bad "backfill-checksums should refuse without confirmation"
docker exec -e DATABASE_URL="postgres://omnira:x@127.0.0.1:5432/ledgertest" -e MIGRATIONS_DIR=/tmp/migrations -e CONFIRM_BACKFILL=1 "$C" sh /tmp/migrate-sql.sh backfill-checksums >"$WORK/backfill.out" 2>&1
grep -q "1 row(s) updated" "$WORK/backfill.out" && ok "backfill-checksums records exactly the one NULL row" || { bad "backfill-checksums did not update exactly 1 row"; cat "$WORK/backfill.out"; }
CMD=verify run_migrate /tmp/migrations >"$WORK/verify5.out" 2>&1
grep -q "verify: OK" "$WORK/verify5.out" && ! grep -q UNKNOWN "$WORK/verify5.out" && ok "verify is fully clean after backfill" || { bad "verify should be fully clean after backfill"; cat "$WORK/verify5.out"; }

echo "== result"
if [ "$FAILS" -eq 0 ]; then echo "${GRN}MIGRATION LEDGER VALIDATED${NC} (idempotency, drift detection, backfill)"; else echo "${RED}$FAILS check(s) failed${NC}"; fi
exit "$FAILS"
