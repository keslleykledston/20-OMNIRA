#!/bin/sh
# migrate-sql.sh — idempotent SQL migration runner (POSIX sh + psql; runs inside postgres:16-alpine).
#
#   migrate-sql.sh up        apply pending migrations/*.up.sql in order, one transaction each,
#                            recorded in schema_migrations
#   migrate-sql.sh down <migration> roll back latest applied migration only
#   migrate-sql.sh status    list applied and pending versions
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

applied() { [ "$(q "SELECT count(*) FROM schema_migrations WHERE version='$1'")" != "0" ]; }
prefix()  { basename "$1" .up.sql | sed 's/_.*//'; }

case "$CMD" in
  status)
    for f in $(ls "$DIR"/*.up.sql | sort); do
      v=$(basename "$f" .up.sql)
      if applied "$v"; then echo "applied  $v"; else echo "pending  $v"; fi
    done
    ;;
  up)
    n=0
    for f in $(ls "$DIR"/*.up.sql | sort); do
      v=$(basename "$f" .up.sql)
      applied "$v" && continue
      if [ -n "${BASELINE_UP_TO:-}" ] && [ "$(prefix "$f")" \< "$BASELINE_UP_TO" -o "$(prefix "$f")" = "$BASELINE_UP_TO" ]; then
        q "INSERT INTO schema_migrations(version) VALUES ('$v')" >/dev/null
        echo "baseline $v"
        continue
      fi
      echo "applying $v"
      # -1 wraps the migration and its bookkeeping row in ONE transaction: all or nothing.
      psql_run -1 -f "$f" -c "INSERT INTO schema_migrations(version) VALUES ('$v')"
      n=$((n + 1))
    done
    echo "migrations up to date ($n applied now)"
    if [ -n "${APP_DB_PASSWORD:-}" ]; then
      printf "ALTER ROLE omnira_app PASSWORD :'pw';\n" | psql_run -v pw="$APP_DB_PASSWORD"
      echo "omnira_app password set from APP_DB_PASSWORD"
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
    echo "usage: $0 up|down <migration>|status" >&2
    exit 2
    ;;
esac
