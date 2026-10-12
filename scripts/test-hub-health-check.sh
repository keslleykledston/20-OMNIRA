#!/usr/bin/env bash
# Disposable proof for scripts/hub-health-check.sh: saved /metrics bodies stand in for api and worker; nothing live is touched.
# Usage: scripts/test-hub-health-check.sh
set -euo pipefail
cd "$(dirname "$0")/.."
WORK=$(mktemp -d); trap 'rm -rf "$WORK"' EXIT
pass=0; fail=0
check() { if [ "$2" = "$3" ]; then echo "PASS: $1"; pass=$((pass+1)); else echo "FAIL: $1 (got '$2', want '$3')"; fail=$((fail+1)); fi; }
NOW=1790000000
export STATE_FILE="$WORK/state.log" CHECK_NOW_EPOCH=$NOW
worker() { # worker <last success age s> [interval s]; written the way Go's %g prints a Unix time: scientific notation (this broke the first run against production)
  printf 'omnira_hub_projector_last_success_timestamp_seconds %s\nomnira_hub_projector_interval_seconds %s\n' "$(awk -v n="$((NOW - $1))" 'BEGIN{printf "%.9e", n}')" "${2:-60}" > "$WORK/worker.txt"
}
api() { printf 'omnira_session_ctx_enforce %s\nomnira_session_ctx_rejections_total %s\n' "${1:-1}" "${2:-0}" > "$WORK/api.txt"; }
run() { set +e; out=$(METRICS_API_FILE="$WORK/api.txt" METRICS_WORKER_FILE="$WORK/worker.txt" "$@" scripts/hub-health-check.sh 2>&1); code=$?; set -e; }
runenv() { set +e; out=$(env METRICS_API_FILE="$WORK/api.txt" METRICS_WORKER_FILE="$WORK/worker.txt" "$@" scripts/hub-health-check.sh 2>&1); code=$?; set -e; }

api; worker 30;  runenv; check "fresh projector is OK" "$code $(echo "$out" | awk '{print $2}')" "0 OK"
worker 130;      runenv; check "projector late (2 intervals) is WARN" "$code $(echo "$out" | awk '{print $2}')" "0 WARN"
worker 200;      runenv; check "projector stale (3+ intervals) is FAIL" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
worker 179;      runenv; check "just under 3 intervals is still WARN" "$code $(echo "$out" | awk '{print $2}')" "0 WARN"
worker 180;      runenv; check "exactly 3 intervals is FAIL (the boundary belongs to the alarm)" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
worker 119;      runenv; check "just under 2 intervals is OK" "$code $(echo "$out" | awk '{print $2}')" "0 OK"
worker 120;      runenv; check "exactly 2 intervals is WARN" "$code $(echo "$out" | awk '{print $2}')" "0 WARN"
worker 30; api 1 3; runenv; check "a refused signed context is FAIL" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
api 0 0;         runenv; check "a process that sees a database outside enforce is FAIL" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
api; echo 'omnira_health_status{component="database"} 1' > "$WORK/worker.txt"; runenv; check "projector expected but never reported is FAIL" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
runenv EXPECT_PROJECTOR=0; check "projector not expected and absent is OK" "$code $(echo "$out" | awk '{print $2}')" "0 OK"
worker 30; runenv EXPECT_DISTRIBUTOR=1; check "distributor expected but never reported is FAIL" "$code $(echo "$out" | awk '{print $2}')" "1 FAIL"
: > "$WORK/api.txt"; : > "$WORK/worker.txt"; set +e; out=$(METRICS_API_FILE="$WORK/api.txt" METRICS_WORKER_FILE="$WORK/worker.txt" scripts/hub-health-check.sh 2>&1); code=$?; set -e
check "nothing readable is WARN (never a false alarm of the platform)" "$code $(echo "$out" | awk '{print $2}')" "0 WARN"
echo "$pass passed, $fail failed"; [ "$fail" = 0 ]
