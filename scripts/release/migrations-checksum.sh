#!/usr/bin/env bash
# migrations-checksum.sh — deterministic sha256 over an ORDERED INVENTORY of
# every migrations/*.up.sql (filename + content digest), not over the raw
# concatenated bytes.
#
# Filename matters: tools/migrate-sql.sh records a migration's FILENAME (not
# its content) as its identity in schema_migrations. Hashing raw concatenated
# bytes would let two different real inventories collide on the same
# checksum — renaming a migration without touching its content leaves a
# byte-concatenation hash unchanged even though migrate-sql.sh would treat
# the renamed file as a brand new pending migration; redistributing SQL
# across adjacent files' boundaries can do the same (achado da revisão
# independente Codex, F3 — see docs/release/IMPLEMENTATION-STATUS.md). This
# version hashes "<filename> <sha256 of that file's content>" lines, one per
# migration, sorted by filename — a rename OR a content change OR a
# boundary shuffle always changes at least one line, so the final digest
# always changes with any of them.
#
# This is still NOT the per-migration checksum ledger F4 builds (persisted
# in the database at apply time, to detect an ALREADY-APPLIED migration
# being rewritten later). This is a point-in-time digest of the current
# inventory, usable TODAY for the release manifest's migrations.
# checksum_bundle field (release/schemas/release-manifest.schema.json).
# Prints "sha256:<64 hex>" on stdout — the exact form the manifest schema
# requires.
#
# Usage: scripts/release/migrations-checksum.sh [migrations_dir]
set -euo pipefail
DIR="${1:-migrations}"

# `find` failing inside the `< <(...)` process substitution below does NOT
# propagate through `set -e` (achado da revisão independente Codex, F3) — a
# missing/unreadable directory would otherwise silently produce the sha256
# of an EMPTY inventory (the well-known "e3b0c442...") and exit 0, as if
# that were a real, valid checksum of zero migrations. Check explicitly
# before anything else runs.
if [ ! -d "$DIR" ]; then
  echo "migrations-checksum: not a directory: ${DIR}" >&2
  exit 1
fi

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  else
    shasum -a 256 | cut -d' ' -f1
  fi
}

inventory=""
count=0
while IFS= read -r -d '' f; do
  name=$(basename "$f")
  content_digest=$(sha256 <"$f")
  inventory+="${name} ${content_digest}"$'\n'
  count=$((count + 1))
done < <(find "$DIR" -maxdepth 1 -name '*.up.sql' -print0 | sort -z)

# An existing-but-empty directory (no *.up.sql at all) would otherwise also
# silently hash to the empty-string digest and exit 0 — same class of bug
# as the missing-directory case above, just one check further down.
if [ "$count" -eq 0 ]; then
  echo "migrations-checksum: no *.up.sql files found in ${DIR}" >&2
  exit 1
fi

digest=$(printf '%s' "$inventory" | sha256)
echo "sha256:${digest}"
