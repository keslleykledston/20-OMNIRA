#!/usr/bin/env bash
# Disposable proof for scripts/deadman-ping.sh. A local Python http.server
# (127.0.0.1 only) plays the external watcher; the clock is injected, so no
# real waiting. No real URL, token, or live log directory is touched.
#
# Usage: scripts/test-deadman-ping.sh
set -euo pipefail
cd "$(dirname "$0")/.."

WORKDIR=$(mktemp -d)
trap 'kill "${CATCHER_PID:-0}" 2>/dev/null || true; rm -rf "$WORKDIR"' EXIT

pass=0
fail=0
check() {
  local desc="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then
    echo "PASS: $desc"
    pass=$((pass + 1))
  else
    echo "FAIL: $desc (got '$got', want '$want')"
    fail=$((fail + 1))
  fi
}

CATCHER_LOG="$WORKDIR/requests.log"; : > "$CATCHER_LOG"
CATCHER_PORT=$(( (RANDOM % 20000) + 20000 ))
cat > "$WORKDIR/catcher.py" <<'PYEOF'
import http.server, os
class H(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        with open(os.environ["CATCHER_LOG"], "a") as f: f.write(self.path + "\n")
        self.send_response(200); self.send_header("Content-Length", "2"); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(os.environ["CATCHER_PORT"])), H).serve_forever()
PYEOF
CATCHER_LOG="$CATCHER_LOG" CATCHER_PORT="$CATCHER_PORT" python3 "$WORKDIR/catcher.py" &
CATCHER_PID=$!
for _ in $(seq 1 25); do curl -s -o /dev/null "http://127.0.0.1:$CATCHER_PORT/" 2>/dev/null && break; sleep 0.2; done
: > "$CATCHER_LOG"

TOKEN="tok$$secret"
export NOTIFY_ENV_FILE=/nonexistent
export DEADMAN_LOG_DIR="$WORKDIR/logs" DEADMAN_BACKUP_LOG="$WORKDIR/backup.log" DEADMAN_LOG="$WORKDIR/deadman.log"
mkdir -p "$DEADMAN_LOG_DIR"
NOW=1790000000
export DEADMAN_NOW_EPOCH=$NOW
FILES=("$DEADMAN_LOG_DIR/nats-jetstream-check.log" "$DEADMAN_LOG_DIR/waha-session-check.log" "$DEADMAN_LOG_DIR/backup-cloud-check.log" "$DEADMAN_BACKUP_LOG")
fresh() { for f in "${FILES[@]}"; do : > "$f"; touch -d "@$((NOW - 60))" "$f"; done; }
age() { touch -d "@$((NOW - $2 * 60))" "$1"; } # age <file> <minutes>
reqs() { wc -l < "$CATCHER_LOG" | tr -d ' '; }
run() { set +e; out=$(scripts/deadman-ping.sh 2>&1); code=$?; set -e; }
URL="http://127.0.0.1:$CATCHER_PORT/ping/$TOKEN"
FAILURL="http://127.0.0.1:$CATCHER_PORT/fail/$TOKEN"

echo "=== Section 1: not configured ==="
fresh; unset DEADMAN_URL DEADMAN_FAIL_URL
run
check "no URL => exit 0" "$code" "0"
check "no URL => no request" "$(reqs)" "0"
check "no URL => logged as not_configured" "$(grep -c 'state=not_configured' "$DEADMAN_LOG")" "1"

echo "=== Section 2: every job alive => exactly one ping ==="
export DEADMAN_URL="$URL"
run
check "healthy => exit 0" "$code" "0"
check "healthy => one request" "$(reqs)" "1"
check "request hit the ping path" "$(grep -c "^/ping/$TOKEN\$" "$CATCHER_LOG")" "1"
check "healthy => logged state=ok" "$(grep -c 'state=ok ping=sent jobs=4' "$DEADMAN_LOG")" "1"

echo "=== Section 3: one stale job => ping withheld ==="
fresh; age "${FILES[0]}" 20
before=$(reqs)
run
check "stale => exit 1" "$code" "1"
check "stale => no new request" "$(reqs)" "$before"
check "stale job is named with its age" "$(grep -c 'nats-jetstream-check:20min>15min' "$DEADMAN_LOG")" "1"
check "output says the ping was withheld" "$(printf '%s' "$out" | grep -c 'ping withheld')" "1"

echo "=== Section 4: thresholds are exact; each job has its own limit ==="
fresh; age "${FILES[0]}" 15; run
check "exactly at the limit (15 min) still passes" "$code" "0"
fresh; age "${FILES[0]}" 16; run
check "one minute over (16) fails" "$code" "1"
fresh; age "${FILES[2]}" 44; run
check "cloud check at 44 min (limit 45) passes" "$code" "0"
fresh; age "${FILES[3]}" 91; run
check "hourly backup at 91 min (limit 90) fails" "$code" "1"

echo "=== Section 5: a missing log counts as stale ==="
fresh; rm -f "${FILES[1]}"
run
check "missing log => exit 1" "$code" "1"
check "missing log is named" "$(grep -c 'waha-session-check:missing' "$DEADMAN_LOG")" "1"

echo "=== Section 6: optional fail URL fires on stale ==="
fresh; age "${FILES[3]}" 200
export DEADMAN_FAIL_URL="$FAILURL"
: > "$CATCHER_LOG"
run
check "fail URL hit exactly once" "$(grep -c "^/fail/$TOKEN\$" "$CATCHER_LOG")" "1"
check "ping URL not hit while stale" "$(grep -c '^/ping/' "$CATCHER_LOG")" "0"
unset DEADMAN_FAIL_URL

echo "=== Section 7: healthy but the ping itself fails ==="
fresh
DEADMAN_URL="http://127.0.0.1:1/ping/$TOKEN" run
check "unreachable watcher => exit 2" "$code" "2"
check "logged as ping_failed" "$(grep -c 'state=ping_failed' "$DEADMAN_LOG")" "1"

echo "=== Section 8: the secret URL/token never reaches stdout or the log ==="
check "token absent from the log" "$(grep -c "$TOKEN" "$DEADMAN_LOG" || true)" "0"
check "token absent from the last output" "$(printf '%s' "$out" | grep -c "$TOKEN" || true)" "0"

bash -n scripts/deadman-ping.sh
echo ""
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
