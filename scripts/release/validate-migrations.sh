#!/usr/bin/env bash
# validate-migrations.sh — static lint of migrations/*.sql (G4, F2's slice of it).
#
# This is NOT the checksum/drift-detection ledger the spec asks for in F4
# (docs/release/spec/..._PACOTE_EXECUCAO.md §F4) — that needs a persisted
# checksum per applied migration and is a bigger, separate piece of work.
# This script only checks what can be verified WITHOUT a database, fast,
# on every PR: every migration is paired, uniquely numbered, and named so
# that lexical sort order (what `ls migrations/*.up.sql | sort` and
# tools/migrate-sql.sh's own globbing use) matches numeric order. Whether
# migrations actually APPLY cleanly is proven separately by
# scripts/test-integration.sh, which builds its template database through
# tools/migrate-sql.sh — this script does not spin up Postgres, on purpose,
# to stay fast and dependency-free for every PR.
#
# Usage: scripts/release/validate-migrations.sh [migrations_dir]
set -euo pipefail

DIR="${1:-migrations}"
if [ ! -d "$DIR" ]; then
  echo "validate-migrations: directory not found: $DIR" >&2
  exit 1
fi

fail=0
note() { echo "validate-migrations: $1" >&2; fail=1; }

# 1. Filename shape: exactly 6 digits, an underscore, lowercase/digits/
# underscores for the name, then .up.sql or .down.sql. Anything else sorts
# unpredictably against the rest (lexical vs numeric) or signals a typo.
pattern='^[0-9]{6}_[a-z0-9_]+\.(up|down)\.sql$'
while IFS= read -r -d '' f; do
  base="$(basename "$f")"
  if ! [[ "$base" =~ $pattern ]]; then
    note "filename does not match ${pattern}: ${base}"
  fi
done < <(find "$DIR" -maxdepth 1 -name '*.sql' -print0)

# 2. Pairing: every .up.sql needs a .down.sql with the exact same stem, and
# vice versa — an orphan in either direction means a half-written migration
# or a forgotten rollback script, either way not safe to ship.
declare -A ups downs
while IFS= read -r -d '' f; do
  stem="$(basename "$f" .up.sql)"
  ups["$stem"]=1
done < <(find "$DIR" -maxdepth 1 -name '*.up.sql' -print0)
while IFS= read -r -d '' f; do
  stem="$(basename "$f" .down.sql)"
  downs["$stem"]=1
done < <(find "$DIR" -maxdepth 1 -name '*.down.sql' -print0)

for stem in "${!ups[@]}"; do
  [ -n "${downs[$stem]:-}" ] || note "missing ${stem}.down.sql (an .up.sql with no matching .down.sql)"
done
for stem in "${!downs[@]}"; do
  [ -n "${ups[$stem]:-}" ] || note "missing ${stem}.up.sql (a .down.sql with no matching .up.sql)"
done

# 3. No duplicate version number across different migration names — two
# migrations racing for the same number is a near-guaranteed merge
# conflict waiting to apply the wrong one first, or silently shadow one.
declare -A seen_version
for stem in "${!ups[@]}"; do
  version="${stem%%_*}"
  if [ -n "${seen_version[$version]:-}" ] && [ "${seen_version[$version]}" != "$stem" ]; then
    note "version ${version} used by both ${seen_version[$version]} and ${stem}"
  fi
  seen_version["$version"]="$stem"
done

count=${#ups[@]}
echo "validate-migrations: ${count} migration(s) checked in ${DIR}"
if [ "$fail" -ne 0 ]; then
  echo "validate-migrations: FAILED" >&2
  exit 1
fi
echo "validate-migrations: OK"
