#!/usr/bin/env bash
# Read-only health check of the Hub's operational signals (docs/ops/HUB-ALERTS.md), read from the /metrics of api and worker. Meant to be wrapped by
# scripts/run-check-with-alert.sh like the other checks. It mutates nothing except its own STATE_FILE log.
#
# Output contract (last line): "<utc-timestamp> <OK|WARN|FAIL> reason=<token> ..."   Exit: 0 OK or WARN, 1 FAIL.
#   FAIL  the Hub inbox projector has not finished a pass without failures for 3 intervals (or never reported while expected): the Hub tab trails the conversations
#   FAIL  the work-pool distributor has not run for 3 intervals (only when EXPECT_DISTRIBUTOR=1)
#   FAIL  a signed session context was refused by the database (key mismatch between a process and the database): in mode enforce those sessions have no access
#   FAIL  the database is not in mode enforce (ADR-0042) as seen by a running process
#   WARN  the projector is late (2 intervals) or the metrics could not be read
#   OK    otherwise
#
# Environment: API_CONTAINER (omnira-api), WORKER_CONTAINER (omnira-worker), EXPECT_PROJECTOR (1), EXPECT_DISTRIBUTOR (0), STATE_FILE.
# Tests inject METRICS_API_FILE / METRICS_WORKER_FILE (a saved /metrics body) and CHECK_NOW_EPOCH.
set -euo pipefail
cd "$(dirname "$0")/.."

API_CONTAINER=${API_CONTAINER:-omnira-api}
WORKER_CONTAINER=${WORKER_CONTAINER:-omnira-worker}
EXPECT_PROJECTOR=${EXPECT_PROJECTOR:-1}
EXPECT_DISTRIBUTOR=${EXPECT_DISTRIBUTOR:-0}
STATE_FILE=${STATE_FILE:-/var/log/omnira/hub-health-check.log}
now=${CHECK_NOW_EPOCH:-$(date -u +%s)}

emit() { # emit <SEVERITY> <detail...>
  local sev="$1"; shift
  local line; line="$(date -u -d "@$now" +%Y-%m-%dT%H:%M:%SZ) $sev $*"
  echo "$line"; echo "$line" >> "$STATE_FILE" 2>/dev/null || true
}

fetch() { # fetch <file override> <container> <port>
  if [ -n "$1" ]; then cat "$1"; else docker exec "$2" wget -q -O - "http://127.0.0.1:$3/metrics" 2>/dev/null; fi
}
api=$(fetch "${METRICS_API_FILE:-}" "$API_CONTAINER" 8080) || api=""
worker=$(fetch "${METRICS_WORKER_FILE:-}" "$WORKER_CONTAINER" 9090) || worker=""
if [ -z "$api" ] && [ -z "$worker" ]; then
  emit WARN "reason=metrics_unreadable"
  exit 0
fi

val() { # val <metrics text> <metric line prefix> -> number or empty
  printf '%s\n' "$1" | awk -v p="$2" 'index($0, p) == 1 { print $NF; exit }'
}
fail=""; warn=""

# 1. signed session context: any refusal, or a process that sees a database not in enforce mode
for who in api worker; do
  body=${!who}
  [ -n "$body" ] || continue
  rej=$(val "$body" 'omnira_session_ctx_rejections_total ')
  if [ -n "$rej" ] && [ "${rej%.*}" -gt 0 ] 2>/dev/null; then fail="$fail session_ctx_refused_by_database($who)=$rej"; fi
  enf=$(val "$body" 'omnira_session_ctx_enforce ')
  if [ -n "$enf" ] && [ "${enf%.*}" = 0 ]; then fail="$fail database_not_in_enforce($who)"; fi
done

# 2. the projector and the distributor live in the worker: freshness = age of the last pass without failures, against the configured interval
fresh() { # fresh <name> <metric prefix> <expect>
  local last interval age
  last=$(val "$worker" "omnira_hub_${2}_last_success_timestamp_seconds ")
  interval=$(val "$worker" "omnira_hub_${2}_interval_seconds ")
  if [ -z "$last" ] || [ -z "$interval" ]; then
    [ "$3" = 1 ] && fail="$fail $1_never_reported"
    return 0
  fi
  last=${last%.*}; interval=${interval%.*}
  age=$((now - last)); [ "$age" -ge 0 ] || age=0
  if [ "$age" -gt $((interval * 3)) ]; then fail="$fail $1_stale_age_s=$age interval_s=$interval"
  elif [ "$age" -gt $((interval * 2)) ]; then warn="$warn $1_late_age_s=$age interval_s=$interval"; fi
}
if [ -n "$worker" ]; then
  fresh projector projector "$EXPECT_PROJECTOR"
  fresh distributor distributor "$EXPECT_DISTRIBUTOR"
else
  warn="$warn worker_metrics_unreadable"
fi

if [ -n "$fail" ]; then emit FAIL "reason=$(echo $fail | tr ' ' ',')"; exit 1; fi
if [ -n "$warn" ]; then emit WARN "reason=$(echo $warn | tr ' ' ',')"; exit 0; fi
emit OK "reason=hub_signals_fresh"
