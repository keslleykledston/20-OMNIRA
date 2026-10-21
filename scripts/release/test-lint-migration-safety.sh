#!/usr/bin/env bash
# test-lint-migration-safety.sh — F4: exercises scripts/release/lint-migration-safety.sh against
# the fixtures in release/fixtures/migration-lint/ (expand_only unless noted) plus a full survey
# of every real migration in the repository, which must stay clean except the one documented,
# pre-existing, already-shipped finding (migrations/000104_hub_work_pools.up.sql — see
# docs/release/IMPLEMENTATION-STATUS.md F4).
#
# No database needed — pure static lint. Usage: scripts/release/test-lint-migration-safety.sh
set -uo pipefail
cd "$(dirname "$0")/../.." || exit 1

GRN=$'\033[32m'; RED=$'\033[31m'; NC=$'\033[0m'; FAILS=0
ok()  { echo "  ${GRN}✓${NC} $*"; }
bad() { echo "  ${RED}✗ $*${NC}"; FAILS=$((FAILS+1)); }

expect_pass() { # expect_pass <file> <strategy>
  if bash scripts/release/lint-migration-safety.sh "$1" "$2" >/dev/null 2>&1; then
    ok "$(basename "$1") / $2: correctly OK"
  else
    bad "$(basename "$1") / $2: should have passed"
  fi
}
expect_fail() { # expect_fail <file> <strategy>
  if bash scripts/release/lint-migration-safety.sh "$1" "$2" >/dev/null 2>&1; then
    bad "$(basename "$1") / $2: should have failed"
  else
    ok "$(basename "$1") / $2: correctly flagged"
  fi
}

echo "== fixtures"
expect_pass release/fixtures/migration-lint/safe-add-with-default.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-add-without-default.up.sql expand_only
expect_fail release/fixtures/migration-lint/contract.up.sql expand_only
expect_pass release/fixtures/migration-lint/contract.up.sql expand_then_contract_later
expect_fail release/fixtures/migration-lint/safe-add-with-default.up.sql no_schema_change
expect_fail release/fixtures/migration-lint/unsafe-inline-not-null.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-cross-table-exemption.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-schema-qualified-cross-table.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-default-null.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-drop-no-column-keyword.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-comma-separated-no-column-keyword.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-drop-default-then-not-null.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-drop-default-from-inline-not-null.up.sql expand_only
expect_pass release/fixtures/migration-lint/safe-set-default-then-not-null.up.sql expand_only
expect_pass release/fixtures/migration-lint/safe-whitespace-variant-set-default.up.sql expand_only
expect_pass release/fixtures/migration-lint/safe-drop-not-null-then-drop-default.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-parenthesized-null-default.up.sql expand_only
expect_fail release/fixtures/migration-lint/unsafe-drop-table-multiline.up.sql expand_only

echo "== full survey of every real migration (expand_only)"
fail_list=()
for f in migrations/*.up.sql; do
  bash scripts/release/lint-migration-safety.sh "$f" expand_only >/dev/null 2>&1 || fail_list+=("$(basename "$f")")
done
expected_findings=("000104_hub_work_pools.up.sql")
if [ "${#fail_list[@]}" -eq "${#expected_findings[@]}" ] && [ "${fail_list[*]}" = "${expected_findings[*]}" ]; then
  ok "real migrations: exactly the documented pre-existing finding (${expected_findings[*]}), nothing else"
else
  bad "real migrations: expected [${expected_findings[*]}], got [${fail_list[*]:-none}]"
fi

echo "== result"
if [ "$FAILS" -eq 0 ]; then echo "${GRN}MIGRATION SAFETY LINT VALIDATED${NC}"; else echo "${RED}$FAILS check(s) failed${NC}"; fi
exit "$FAILS"
