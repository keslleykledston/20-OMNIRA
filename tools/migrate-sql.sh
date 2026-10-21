#!/bin/sh
# migrate-sql.sh — idempotent SQL migration runner (POSIX sh + psql; runs inside postgres:16-alpine).
#
#   migrate-sql.sh up        apply pending migrations/*.up.sql in order, one transaction each,
#                            recorded in schema_migrations together with a sha256 of the file
#                            content at the moment it was applied
#   migrate-sql.sh down <migration> roll back latest applied migration only
#   migrate-sql.sh status    list applied and pending versions, with checksum status
#   migrate-sql.sh verify    compare the checksum recorded for every APPLIED migration against
#                            the migrations/*.up.sql file on disk today; fails (exit 1) on any
#                            MODIFIED or MISSING file (F4, docs/release/spec/..._PACOTE_EXECUCAO.md
#                            §8: "não modificar arquivo .up.sql já aplicado"). A row with no
#                            recorded checksum (applied before this ledger existed) is reported as
#                            UNKNOWN, informational only — never auto-filled, never a failure.
#   migrate-sql.sh backfill-checksums  explicit, operator-invoked, opt-in: records today's
#                            sha256 for rows above with no recorded checksum (never run by `up`,
#                            `status`, `verify`, or any automation). Requires
#                            CONFIRM_BACKFILL=1 and prints exactly what it assumed. This does NOT
#                            "rewrite" an already-applied migration — it gives a ledger row that
#                            was created before this feature its first-ever checksum value, on
#                            the explicit (unverifiable, stated) assumption that the file on disk
#                            today is the same one that was actually applied back then.
#
# Connection: standard libpq env (PGHOST, PGPORT, PGUSER, PGPASSWORD, PGDATABASE) or DATABASE_URL.
# Must connect as the schema OWNER (not omnira_app): migrations create roles/policies/functions.
#
# Env:
#   MIGRATIONS_DIR      default /migrations
#   BASELINE_UP_TO      version prefix (e.g. 000027): mark every migration <= it as applied WITHOUT
#                       running it. Use once to adopt a database whose schema was applied by hand.
#   APP_DB_PASSWORD     if set, sets the password of the application role omnira_app after migrating
#                       (migration 000006 creates it with the development password "omnira_app").
#   OMNIRA_SESSION_CTX_KEY  ADR-0042: hex key (>= 64 hex characters) the application signs its session contexts with; installed in the database after migrating
#                       (the previous key stays accepted until ctx_drop_previous_key, so a rotation never logs anybody out). Same variable the api and worker read.
#   SESSION_CTX_MODE    off|measure|enforce: also set the verification mode (the gate uses it; operations use scripts/session-ctx.sh).
set -eu
export PGOPTIONS="${PGOPTIONS:-} -c client_min_messages=warning"

DIR="${MIGRATIONS_DIR:-/migrations}"
CMD="${1:-up}"

psql_run() {
  if [ -n "${DATABASE_URL:-}" ]; then
    psql "$DATABASE_URL" -X -q -v ON_ERROR_STOP=1 "$@"
  else
    psql -X -q -v ON_ERROR_STOP=1 "$@"
  fi
}
q() { psql_run -tA -c "$1"; }

psql_run -c "CREATE TABLE IF NOT EXISTS schema_migrations (
  version TEXT PRIMARY KEY,
  applied_at TIMESTAMPTZ NOT NULL DEFAULT now())"
# Additive, idempotent: existing rows (applied before this column existed) get checksum=NULL and
# are never touched again by this statement or by anything in `up` — only `backfill-checksums`,
# run deliberately by an operator, ever sets a checksum on a pre-existing row.
psql_run -c "ALTER TABLE schema_migrations ADD COLUMN IF NOT EXISTS checksum TEXT"

applied() { [ "$(q "SELECT count(*) FROM schema_migrations WHERE version='$1'")" != "0" ]; }
prefix()  { basename "$1" .up.sql | sed 's/_.*//'; }
sha256() {
  if command -v sha256sum >/dev/null 2>&1; then
    sha256sum | cut -d' ' -f1
  elif command -v shasum >/dev/null 2>&1; then
    shasum -a 256 | cut -d' ' -f1
  else
    openssl dgst -sha256 -r | cut -d' ' -f1
  fi
}

# check_drift <verbose:0|1> — shared by `verify` (always verbose) and `up` (quiet: only the
# migrations that are wrong are worth printing before every routine apply). Returns 1 if any
# applied migration's file was modified or removed since it was applied; UNKNOWN (no recorded
# checksum — applied before this ledger existed) is always informational, never a failure.
check_drift() {
  verbose="$1"
  bad=0
  # Captured into a variable and checked BEFORE the loop reads it: a query that fails (dropped
  # connection, permissions) inside a `done <<EOF / $(...) / EOF` here-document would otherwise
  # feed the loop zero lines, indistinguishable from "nothing applied yet" — the loop would exit
  # having verified nothing, and the caller would see a clean (non-)result. set -e does not catch
  # this: the failing command runs inside a command substitution that is itself just a
  # here-document body, not a checked statement on its own.
  ledger_rows=$(q "SELECT version || '|' || coalesce(checksum, '') FROM schema_migrations ORDER BY version") || {
    echo "migrate-sql: could not read schema_migrations" >&2
    return 1
  }
  while IFS='|' read -r v sum; do
    [ -n "$v" ] || continue
    f="$DIR/$v.up.sql"
    if [ -z "$sum" ]; then
      [ "$verbose" = 1 ] && echo "UNKNOWN  $v  (applied before the checksum ledger existed; not verifiable)"
      continue
    fi
    if [ ! -f "$f" ]; then
      echo "MISSING  $v  (recorded checksum $sum, but $f no longer exists)"
      bad=1
      continue
    fi
    now=$(sha256 <"$f")
    if [ "$now" = "$sum" ]; then
      [ "$verbose" = 1 ] && echo "OK       $v"
    else
      echo "MODIFIED $v  (recorded $sum, file on disk now $now)"
      bad=1
    fi
  done <<EOF
$ledger_rows
EOF
  return "$bad"
}

case "$CMD" in
  status)
    for f in $(ls "$DIR"/*.up.sql | sort); do
      v=$(basename "$f" .up.sql)
      if applied "$v"; then
        sum=$(q "SELECT coalesce(checksum, '') FROM schema_migrations WHERE version='$v'")
        if [ -n "$sum" ]; then echo "applied  $v  checksum=${sum}"; else echo "applied  $v  checksum=unknown"; fi
      else
        echo "pending  $v"
      fi
    done
    ;;
  verify)
    # Drift detection (F4): an applied migration's checksum must still match the file on disk.
    if check_drift 1; then
      echo "migrate-sql verify: OK"
    else
      echo "migrate-sql verify: FAILED — an applied migration file was modified or removed after being applied" >&2
      exit 1
    fi
    ;;
  backfill-checksums)
    [ "${CONFIRM_BACKFILL:-0}" = "1" ] || {
      echo "backfill-checksums: refusing without CONFIRM_BACKFILL=1 (this is an explicit, operator-invoked action — see usage comment)" >&2
      exit 2
    }
    n=0
    # Same fix as `verify` above: capture and check the query before the loop reads it, so a
    # failed SELECT can never look like "zero rows to backfill" and report success.
    null_rows=$(q "SELECT version FROM schema_migrations WHERE checksum IS NULL ORDER BY version") || {
      echo "backfill-checksums: FAILED — could not read schema_migrations" >&2
      exit 1
    }
    while IFS= read -r v; do
      [ -n "$v" ] || continue
      f="$DIR/$v.up.sql"
      if [ ! -f "$f" ]; then
        echo "backfill-checksums: SKIP $v — $f no longer exists, cannot assume its current content" >&2
        continue
      fi
      sum=$(sha256 <"$f")
      q "UPDATE schema_migrations SET checksum='$sum' WHERE version='$v' AND checksum IS NULL" >/dev/null
      echo "backfill-checksums: recorded $sum for $v (assumed: $f today is the file that was applied)"
      n=$((n + 1))
    done <<EOF
$null_rows
EOF
    echo "backfill-checksums: ${n} row(s) updated"
    ;;
  up)
    # F4 (Codex review): drift on an already-applied migration must block normal `up`, not only
    # a separate `verify` invocation an operator might never run. Checked BEFORE any pending
    # migration is applied — installation.
    if ! check_drift 0; then
      echo "migrate-sql up: REFUSING — an already-applied migration file was modified or removed (see the lines above); run '$0 verify' for the full picture" >&2
      exit 1
    fi
    n=0
    for f in $(ls "$DIR"/*.up.sql | sort); do
      v=$(basename "$f" .up.sql)
      applied "$v" && continue
      sum=$(sha256 <"$f")
      if [ -n "${BASELINE_UP_TO:-}" ] && [ "$(prefix "$f")" \< "$BASELINE_UP_TO" -o "$(prefix "$f")" = "$BASELINE_UP_TO" ]; then
        # This writes a ledger row for the first time (it did not exist before this command),
        # so recording today's checksum here is not "rewriting an already-applied migration" —
        # it is the row's birth, same as a real `up` application below.
        q "INSERT INTO schema_migrations(version, checksum) VALUES ('$v', '$sum')" >/dev/null
        echo "baseline $v"
        continue
      fi
      echo "applying $v"
      # -1 wraps the migration and its bookkeeping row in ONE transaction: all or nothing.
      psql_run -1 -f "$f" -c "INSERT INTO schema_migrations(version, checksum) VALUES ('$v', '$sum')"
      n=$((n + 1))
    done
    echo "migrations up to date ($n applied now)"
    if [ -n "${APP_DB_PASSWORD:-}" ]; then
      printf "ALTER ROLE omnira_app PASSWORD :'pw';\n" | psql_run -v pw="$APP_DB_PASSWORD"
      echo "omnira_app password set from APP_DB_PASSWORD"
    fi
    if [ -n "${OMNIRA_SESSION_CTX_KEY:-}" ]; then
      case "$OMNIRA_SESSION_CTX_KEY" in
        *[!0-9a-fA-F]*) echo "OMNIRA_SESSION_CTX_KEY must be hex" >&2; exit 2 ;;
      esac
      [ "${#OMNIRA_SESSION_CTX_KEY}" -ge 64 ] || { echo "OMNIRA_SESSION_CTX_KEY must have at least 64 hex characters (32 bytes)" >&2; exit 2; }
      # libraries that log statement parameters on their own (pgaudit, auto_explain) are out of the installer's reach: refuse, unless the operator accepts the risk and rotates afterwards
      libs=$(q "SELECT concat_ws(',', current_setting('shared_preload_libraries'), current_setting('session_preload_libraries'), current_setting('local_preload_libraries'))")
      case "$libs" in
        *pgaudit*|*auto_explain*)
          if [ -z "${SESSION_CTX_ALLOW_LOGGED_KEY:-}" ]; then
            echo "preloaded libraries ($libs) can log statement parameters: installing OMNIRA_SESSION_CTX_KEY would expose it (SESSION_CTX_ALLOW_LOGGED_KEY=1 and rotate the key afterwards)" >&2; exit 2
          fi ;;
      esac
      # the installing session itself logs nothing about the statement or its parameter (the server's own settings are overridden for this session only), and the key goes as a BOUND parameter
      # through stdin (psql \bind: extended protocol): neither in a process listing nor in the statement text
      res=$({ printf "SET log_statement = 'none'; SET log_min_duration_statement = -1; SET log_min_duration_sample = -1; SET log_transaction_sample_rate = 0; SET log_statement_sample_rate = 0; SET log_parameter_max_length = 0; SET log_parameter_max_length_on_error = 0;\n"
        printf 'SELECT public.ctx_install_key(decode($1, %s)) \\bind %s \\g\n' "'hex'" "$OMNIRA_SESSION_CTX_KEY"; } | psql_run -tA)
      echo "session context key: $res"
    fi
    if [ -n "${SESSION_CTX_MODE:-}" ]; then
      echo "session context mode: $(q "SELECT public.ctx_set_mode('$SESSION_CTX_MODE')")"
    fi
    ;;
  down)
    v="${2:-}"
    [ -n "$v" ] || { echo "usage: $0 down <migration>" >&2; exit 2; }
    f="$DIR/$v.down.sql"
    [ -f "$f" ] || { echo "down migration not found: $v" >&2; exit 2; }
    current="$(q "SELECT version FROM schema_migrations ORDER BY version DESC LIMIT 1")"
    [ "$current" = "$v" ] || { echo "refusing down: $v is not latest applied migration ($current)" >&2; exit 2; }
    echo "rolling back $v"
    psql_run -1 -f "$f" -c "DELETE FROM schema_migrations WHERE version='$v'"
    ;;
  *)
    echo "usage: $0 up|down <migration>|status|verify|backfill-checksums" >&2
    exit 2
    ;;
esac
