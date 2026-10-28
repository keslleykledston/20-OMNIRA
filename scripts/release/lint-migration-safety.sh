#!/usr/bin/env bash
# lint-migration-safety.sh — F4 (docs/release/spec/..._PACOTE_EXECUCAO.md §3/§8):
# a migration's DDL must not contradict the database_strategy a release declares for it
# (release/policies/compatibility.yaml). "expand_only"/"no_schema_change" releases must never
# ship a statement that breaks code from a PREVIOUS release still reading/writing the old shape;
# only "expand_then_contract_later" is allowed to remove what an earlier expand made obsolete.
#
# This is a conservative, line-based heuristic over the SQL text — not a real parser. It exists
# to catch an obviously-contract statement landing in a release that never declared one, not to
# replace human review of a migration's actual effect. False negatives are possible (e.g. a
# destructive change hidden inside a DO block or a function body); it is intentionally biased
# toward flagging anything that LOOKS like a contract rather than trying to prove one.
#
# Known, accepted limitation (11 rounds of independent review converged here — see
# docs/release/IMPLEMENTATION-STATUS.md F4): a column's default/NOT NULL state is evaluated
# sub-clause by sub-clause within a statement, not as the single atomic end-state Postgres
# actually applies. A migration that writes, in ONE statement,
# "ALTER COLUMN x DROP DEFAULT, ALTER COLUMN x SET DEFAULT 2" is flagged even though Postgres
# never exposes an intermediate "NOT NULL with no default" state — nobody writes it that way in
# practice (the natural, equally safe form is just "ALTER COLUMN x SET DEFAULT 2", which this
# lint already accepts). This is the lint's documented bias working as intended: an occasional
# over-strict rejection of a contrived pattern, never an under-strict pass of a real contract.
#
# Usage: scripts/release/lint-migration-safety.sh <migration.up.sql> <database_strategy>
#   database_strategy: no_schema_change | expand_only | expand_then_contract_later
#     (must be one of release/policies/compatibility.yaml's database_strategies keys)
# Exit: 0 clean, 1 a contract-shaped statement was found under a strategy that forbids it,
#       2 usage/argument error.
set -euo pipefail

FILE="${1:-}"
STRATEGY="${2:-}"

if [ -z "$FILE" ] || [ -z "$STRATEGY" ]; then
  echo "usage: $0 <migration.up.sql> <database_strategy>" >&2
  exit 2
fi
if [ ! -f "$FILE" ]; then
  echo "lint-migration-safety: file not found: $FILE" >&2
  exit 2
fi
case "$STRATEGY" in
  no_schema_change | expand_only | expand_then_contract_later) ;;
  *)
    echo "lint-migration-safety: unknown database_strategy '$STRATEGY' (see release/policies/compatibility.yaml)" >&2
    exit 2
    ;;
esac

# Strip SQL line comments (--...) and block comments (/*...*/) before matching, so a destructive
# statement mentioned only in a comment (explaining why one was NOT used, for instance) never
# produces a false positive.
strip_comments() {
  sed -E 's/--.*$//' "$1" | perl -0777 -pe 's{/\*.*?\*/}{}gs' 2>/dev/null ||
    sed -E 's/--.*$//' "$1"
}
body="$(strip_comments "$FILE")"

# database_strategy: no_schema_change — the release declares ZERO schema change, so ANY DDL at
# all here contradicts what it told the planner.
if [ "$STRATEGY" = "no_schema_change" ]; then
  if printf '%s\n' "$body" | grep -qiE '\b(CREATE|ALTER|DROP|TRUNCATE)\b'; then
    echo "lint-migration-safety: ${FILE}: contains DDL, but database_strategy=no_schema_change declares none" >&2
    exit 1
  fi
  echo "lint-migration-safety: ${FILE}: OK (no_schema_change, no DDL found)"
  exit 0
fi

# Contract-shaped patterns: things that break code from an earlier release still reading/writing
# the PREVIOUS shape. Allowed only when database_strategy=expand_then_contract_later.
declare -a hits=()
# `grep` (without -z) matches one LINE at a time: "DROP\s+TABLE" never matches when a migration
# formats "DROP" and "TABLE widgets" on separate lines, even though \s itself can represent a
# newline — grep simply never hands the regex engine a string containing one (Codex review).
# Collapsing all whitespace runs (including real newlines) to a single space before these
# checks makes line-break formatting irrelevant, the same way the perl-based checks below are
# already immune to it via -0777 slurp mode.
body_oneline="$(printf '%s' "$body" | tr '\n\t' '  ' | tr -s ' ')"
check() { # check <label> <regex>
  if printf '%s\n' "$body_oneline" | grep -qiE "$2"; then
    hits+=("$1")
  fi
}
check "DROP TABLE"                '\bDROP\s+TABLE\b'
check "RENAME COLUMN"             '\bRENAME\s+COLUMN\b'
check "RENAME TABLE/TO"           '\bRENAME\s+TO\b'
check "DROP TYPE value (enum narrowing)" '\bALTER\s+TYPE\b.*\bDROP\s+VALUE\b'

# TRUNCATE as a real statement — identified by what follows it (an identifier, optionally after
# TABLE), not by excluding GRANT/REVOKE lines (which, on the now whitespace-collapsed single
# line, would wrongly blank out the whole check if GRANT/REVOKE appeared ANYWHERE in the file).
# "REVOKE DELETE, TRUNCATE ON t FROM app_role" has TRUNCATE followed by "ON", never an
# identifier directly — a tightening of privileges, never a contract.
if printf '%s\n' "$body_oneline" | grep -qiP '\bTRUNCATE\s+(TABLE\s+)?(?!ON\b)[A-Za-z_"]'; then
  hits+=("TRUNCATE")
fi

# A NOT NULL column is only a safe expand when it is compatible with an OLDER release's INSERT
# that doesn't know about it — i.e. the column has a DEFAULT. Two shapes, both table-and-column
# scoped (Codex's review: a bare column-name match let an unrelated table's DEFAULT exempt a
# different table's SET NOT NULL) and order-respecting (a column is only "safe" once an EARLIER
# statement in this same file actually gave it that default):
#   1. ADD COLUMN <col> ... NOT NULL ... with no DEFAULT in the same clause — a contract the
#      moment it runs, even on an empty table today (any later row, or any older release still
#      inserting without this column, breaks immediately).
#   2. ALTER COLUMN <col> SET NOT NULL on a column that either pre-existed this file, or was
#      ADD COLUMN'd earlier in THIS file without a DEFAULT. This is exactly the shape of the
#      real, already-shipped migrations/000104_hub_work_pools.up.sql (ADD COLUMN hub_id UUID
#      [no default], backfill, SET NOT NULL) — a pre-existing expand/contract gap from before
#      this program, documented in docs/release/IMPLEMENTATION-STATUS.md F4, not retroactively
#      "fixed" here: the file is already applied in production and rewriting it is forbidden.
# Statements are split on ';' (heuristic — this codebase's migrations never nest a literal ';'
# inside a string/DEFAULT expression) so each one resolves to the ALTER TABLE it belongs to.
not_null_hits="$(printf '%s' "$body" | perl -0777 -ne '
  my %safe;    # "table.column" => 1 while a usable (non-NULL) DEFAULT is currently in effect
  my %notnull; # "table.column" => 1 while the column is currently NOT NULL, by any means
  for my $stmt (split /;/, $_) {
    next unless $stmt =~ /\S/;
    # Capture the FULL identifier including an optional schema prefix (public.foo) — matching
    # only the first segment would collapse every table in the same schema onto one exemption
    # key, letting a DEFAULT on public.a wrongly exempt a SET NOT NULL on public.b (Codex review).
    next unless $stmt =~ /\bALTER\s+TABLE\s+(?:ONLY\s+)?("?[A-Za-z_][A-Za-z0-9_]*"?(?:\s*\.\s*"?[A-Za-z_][A-Za-z0-9_]*"?)?)/is;
    (my $table = lc($1)) =~ s/"//g;

    # Any comma followed by ADD/DROP/ALTER starts a NEW sub-clause — with or without an explicit
    # COLUMN/CONSTRAINT keyword, since Postgres makes COLUMN optional throughout ALTER TABLE
    # ("ALTER TABLE accounts DROP email" is valid). One stop condition for every span below,
    # instead of enumerating each keyword combination one Codex round at a time.
    my $stop = qr/,\s*(?:ADD|DROP|ALTER)\b/i;

    # ADD [COLUMN] [IF NOT EXISTS] <col> ... — the negative lookahead excludes the other ADD
    # <keyword> sub-clauses (CONSTRAINT/PRIMARY KEY/FOREIGN KEY/CHECK/UNIQUE/EXCLUDE), which
    # would otherwise be misread as a column add once COLUMN is no longer mandatory.
    while ($stmt =~ /\bADD\s+(?:COLUMN\s+)?(?:IF\s+NOT\s+EXISTS\s+)?(?!CONSTRAINT\b|PRIMARY\b|FOREIGN\b|CHECK\b|UNIQUE\b|EXCLUDE\b)"?([A-Za-z_][A-Za-z0-9_]*)"?\b((?:(?!$stop).)*)/gis) {
      my ($col, $span) = (lc($1), $2);
      # "DEFAULT NULL" is not a compatible default: an INSERT from an older release that omits
      # this column would still get NULL for it, which the very NOT NULL this clause adds then
      # rejects. Only a DEFAULT followed by something other than NULL actually supplies a value.
      # "DEFAULT (NULL)" (parenthesized) is the same disguised-NULL case as bare "DEFAULT NULL"
      # (Codex review). This lint stops here deliberately: it is a documented heuristic, not a
      # SQL expression parser, and chasing every further disguise (CAST(NULL AS ...), nested
      # parens, a function that happens to return NULL) has no end — those remain a known,
      # accepted limitation rather than scope for an indefinitely deep regex arms race.
      my $has_default = ($span =~ /\bDEFAULT\b(?!\s+NULL\b)(?!\s*\(\s*NULL\s*\))/i) ? 1 : 0;
      my $has_notnull = ($span =~ /\bNOT\s+NULL\b/i) ? 1 : 0;
      my $key = "${table}.${col}";
      $notnull{$key} = 1 if $has_notnull;
      if ($has_notnull && !$has_default) {
        print "ADD COLUMN ${key} NOT NULL with no default\n";
      } elsif ($has_default) {
        $safe{$key} = 1;
      }
    }

    # ALTER COLUMN <col> SET NOT NULL / DROP DEFAULT / SET DEFAULT NULL / SET DEFAULT <value>.
    # COLUMN is required here (unlike ADD/DROP) because every real migration in this repository
    # always writes it explicitly — see the full survey in
    # scripts/release/test-lint-migration-safety.sh — and making it optional would risk
    # misreading an unrelated ALTER TABLE sub-clause as a column reference without a concrete
    # case demonstrating the need. The alternatives are tried in this order so a literal
    # "SET DEFAULT NULL" is matched by the NULL-specific branch before the generic
    # "SET DEFAULT" fallback below it ever gets a chance to (Perl tries alternatives left to
    # right at each position, not longest-match-first).
    while ($stmt =~ /\bALTER\s+COLUMN\s+"?([A-Za-z_][A-Za-z0-9_]*)"?\s+(SET\s+NOT\s+NULL|DROP\s+NOT\s+NULL|DROP\s+DEFAULT|SET\s+DEFAULT\s+NULL\b|SET\s+DEFAULT\s*\(\s*NULL\s*\)|SET\s+DEFAULT\b)/gis) {
      my $col = lc($1);
      # Normalized to single spaces before any comparison: the regex above tolerates arbitrary
      # whitespace between keywords (tabs, newlines, repeated spaces, all valid SQL), but `eq`
      # against a literal like "SET DEFAULT" is not — a real "SET  DEFAULT 2" (two spaces) was
      # previously misrouted into the default-REMOVAL branch below (Codex review). "SET DEFAULT
      # (NULL)" (parenthesized) is captured as its own, longer alternative above so it can never
      # `eq "SET DEFAULT"` below — it falls into the same disguised-NULL branch as the bare form
      # without needing a separate normalization step.
      (my $action = uc($2)) =~ s/\s+/ /g;
      my $key = "${table}.${col}";
      if ($action =~ /^SET NOT NULL/) {
        print "SET NOT NULL on ${key} (pre-existing, or added without a compatible default)\n" unless $safe{$key};
        $notnull{$key} = 1;
      } elsif ($action =~ /^DROP NOT NULL/) {
        # Loosens the column back to nullable — an older release omitting it is fine again,
        # regardless of whether a default currently exists (Codex review).
        delete $notnull{$key};
      } elsif ($action eq "SET DEFAULT") {
        # A non-NULL default assigned via ALTER COLUMN gives the same compatibility guarantee
        # as an inline "ADD COLUMN ... DEFAULT ..." — a later SET NOT NULL on this column is
        # then a safe expand (Codex review: this was previously only tracked for inline defaults).
        $safe{$key} = 1;
      } else {
        # DROP DEFAULT / SET DEFAULT NULL revokes whatever compatible default this column had.
        # If the column is NOT NULL right now — whether from an earlier SET NOT NULL or from an
        # inline "ADD COLUMN ... NOT NULL ... DEFAULT ..." in the SAME migration — removing its
        # only compatible default breaks every subsequent INSERT from an older release that
        # omits this column, even with no later SET NOT NULL to trip the check above.
        print "DROP DEFAULT on ${key}, which is NOT NULL with no other default\n" if $notnull{$key};
        delete $safe{$key};
      }
    }

    # DROP [COLUMN] [IF EXISTS] <name> — the only other things that can follow a bare DROP at
    # this level are CONSTRAINT (handled separately, never a column) and, on an ALTER COLUMN
    # sub-clause, DROP DEFAULT / DROP NOT NULL (a loosening, never a contract) — both excluded
    # below so this only ever matches an actual column removal.
    while ($stmt =~ /\bDROP\s+(?:COLUMN\s+)?(?:IF\s+EXISTS\s+)?(?!CONSTRAINT\b|DEFAULT\b|NOT\s+NULL\b)"?([A-Za-z_][A-Za-z0-9_]*)"?/gis) {
      my $col = lc($1);
      print "DROP COLUMN ${table}.${col}\n";
    }
  }
' 2>/dev/null)"
while IFS= read -r line; do
  [ -n "$line" ] && hits+=("$line")
done <<EOF
$not_null_hits
EOF

if [ "${#hits[@]}" -eq 0 ]; then
  echo "lint-migration-safety: ${FILE}: OK (${STRATEGY}, no contract-shaped statement found)"
  exit 0
fi

if [ "$STRATEGY" = "expand_then_contract_later" ]; then
  echo "lint-migration-safety: ${FILE}: OK (expand_then_contract_later allows a contract): ${hits[*]}"
  exit 0
fi

echo "lint-migration-safety: ${FILE}: contract-shaped statement(s) [${hits[*]}] found, but database_strategy=${STRATEGY} does not allow a contract (release/policies/compatibility.yaml: allows_contract=false)" >&2
exit 1
