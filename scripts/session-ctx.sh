#!/usr/bin/env bash
# ADR-0042 operations: the signed session context of the database (migration 000118).
#
#   scripts/session-ctx.sh status                 mode, keys (ages only, never the key), recent divergences
#   scripts/session-ctx.sh gen-key                prints a new random key (64 hex chars); store it as OMNIRA_SESSION_CTX_KEY in the environment of api and worker
#   scripts/session-ctx.sh install-key            installs OMNIRA_SESSION_CTX_KEY (from the environment or .env) as the CURRENT key; the previous current key becomes "previous"
#                                                 and stays accepted, so a rotation logs nobody out
#   scripts/session-ctx.sh drop-previous          retires the previous key (after every api/worker instance runs with the new one)
#   scripts/session-ctx.sh mode off|measure|enforce
#   scripts/session-ctx.sh divergences [minutes]  count and sample of "session_ctx divergence" warnings in the database log (mode measure), default 10 minutes
#
# Rollout (see docs/adr/0042-unforgeable-session-context.md): install-key -> restart api+worker with the same key in their environment -> mode measure ->
# watch divergences until it stays at zero under real traffic -> mode enforce. Rolling back is `mode off` (instant, no restart).
set -euo pipefail
cd "$(dirname "$0")/.."
PG_CONTAINER=${PG_CONTAINER:-omnira-postgres}; PG_OWNER=${PG_OWNER:-omnira}; DB_NAME=${DB_NAME:-omnira_dev}
owner() { docker exec -i "$PG_CONTAINER" psql -U "$PG_OWNER" -d "$DB_NAME" -X -q -v ON_ERROR_STOP=1 "$@"; }

load_key() {
  local k="${OMNIRA_SESSION_CTX_KEY:-}"
  if [ -z "$k" ] && [ -f .env ]; then k=$(grep -E '^OMNIRA_SESSION_CTX_KEY=' .env | tail -1 | cut -d= -f2- | tr -d "\"' " || true); fi
  [ -n "$k" ] || { echo "OMNIRA_SESSION_CTX_KEY is not set (environment or .env)" >&2; exit 2; }
  case "$k" in *[!0-9a-fA-F]*) echo "OMNIRA_SESSION_CTX_KEY must be hex" >&2; exit 2 ;; esac
  [ "${#k}" -ge 64 ] || { echo "OMNIRA_SESSION_CTX_KEY must have at least 64 hex characters" >&2; exit 2; }
  printf '%s' "$k"
}

case "${1:-status}" in
  gen-key) openssl rand -hex 32 ;;
  install-key)
    k=$(load_key)
    # the key travels inside a statement (through stdin, never in a process listing): refuse when the server logs statements, unless told otherwise
    ls=$(owner -tA -c "SHOW log_statement")
    if [ "$ls" != "none" ] && [ -z "${FORCE:-}" ]; then
      echo "log_statement is '$ls': the key would be written to the database log. Set it to none (or FORCE=1 and rotate the key afterwards)." >&2; exit 2
    fi
    printf "SELECT public.ctx_install_key(decode('%s','hex'));\n" "$k" | owner -tA ;;
  drop-previous) owner -tA -c "SELECT public.ctx_drop_previous_key()" | sed 's/^/previous keys dropped: /' ;;
  mode)
    m="${2:-}"; case "$m" in off|measure|enforce) ;; *) echo "usage: $0 mode off|measure|enforce" >&2; exit 2 ;; esac
    owner -tA -c "SELECT public.ctx_set_mode('$m')" | sed 's/^/mode: /' ;;
  divergences)
    mins="${2:-10}"
    n=$(docker logs --since "${mins}m" "$PG_CONTAINER" 2>&1 | grep -c "session_ctx divergence" || true)
    echo "divergences in the last ${mins} min: $n"
    docker logs --since "${mins}m" "$PG_CONTAINER" 2>&1 | grep "session_ctx divergence" | sed -E 's/^.*(session_ctx divergence: [a-z_]+).*/\1/' | sort | uniq -c | sort -rn | head ;;
  status)
    owner -tA -F' ' -c "SELECT 'mode: ' || mode || ' (changed ' || to_char(changed_at,'YYYY-MM-DD HH24:MI') || ')' FROM session_ctx_mode"
    owner -tA -F' ' -c "SELECT 'key ' || state || ': installed ' || to_char(created_at,'YYYY-MM-DD HH24:MI') || ', ' || octet_length(key) || ' bytes' FROM session_ctx_keys ORDER BY state"
    "$0" divergences 10 ;;
  *) sed -n '2,15p' "$0"; exit 2 ;;
esac
