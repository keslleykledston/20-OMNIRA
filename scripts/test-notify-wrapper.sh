#!/usr/bin/env bash
# PILOT.4E1: disposable proof for scripts/run-check-with-alert.sh +
# scripts/lib/notify.sh. Same spirit as scripts/test-integration.sh — every
# resource here is created and destroyed by this one invocation.
#
# Never talks to the real ntfy.sh (no credential is created or needed): a
# local Python http.server stands in for it, bound to 127.0.0.1 only, and
# records each request it receives (including the JSON body) to a log file
# this script asserts against.
#
# Never touches the real OMNIRA_JOBS stream or the real WAHA session: the
# "real check integration" scenarios point scripts/nats-jetstream-check.sh at
# an intentionally-invalid, unused local port and
# scripts/waha-session-check.sh at a nonexistent container name. All other
# state-machine scenarios (dedup, reminder, severity-change, WARN semantics,
# recovery, concurrent-run locking) use small local fake-check scripts that
# emit the exact same "<timestamp> <SEVERITY> ..." contract the real checks
# use — this proves the WRAPPER's own logic exhaustively and deterministically
# without needing to calibrate real JetStream utilization thresholds.
#
# Usage: scripts/test-notify-wrapper.sh
set -euo pipefail
cd "$(dirname "$0")/.."

WORKDIR=$(mktemp -d)
trap 'kill "${CATCHER_PID:-0}" 2>/dev/null || true; rm -rf "$WORKDIR"' EXIT

CATCHER_LOG="$WORKDIR/catcher_requests.log"
CATCHER_PORT=$(( (RANDOM % 20000) + 20000 ))
CATCHER_MODE_FILE="$WORKDIR/catcher_mode" # "200" (default) or "500"
echo 200 > "$CATCHER_MODE_FILE"

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

# --- disposable fake-ntfy HTTP catcher -----------------------------------
cat > "$WORKDIR/catcher.py" <<'PYEOF'
import http.server, os, json

log_path = os.environ["CATCHER_LOG"]
mode_path = os.environ["CATCHER_MODE_FILE"]

class Handler(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length).decode("utf-8", "replace")
        # notify.sh publishes to the root endpoint with a JSON body (Section
        # 11: topic/title/message/priority/tags all live in the body, fed to
        # curl via stdin — never as headers or URL/argv content).
        try:
            payload = json.loads(body)
        except Exception:
            payload = {}
        with open(log_path, "a") as f:
            f.write("PATH=%s TITLE=%s PRIORITY=%s TAGS=%s BODY=%s\n" % (
                self.path,
                payload.get("title", ""),
                payload.get("priority", ""),
                ",".join(payload.get("tags", [])),
                body.replace("\n", " "),
            ))
        try:
            with open(mode_path) as m:
                code = int(m.read().strip() or "200")
        except Exception:
            code = 200
        self.send_response(code)
        self.send_header("Content-Length", "2")
        self.end_headers()
        self.wfile.write(b"ok")

    def log_message(self, *args):
        pass

http.server.HTTPServer(("127.0.0.1", int(os.environ["CATCHER_PORT"])), Handler).serve_forever()
PYEOF

start_catcher() {
  CATCHER_LOG="$CATCHER_LOG" CATCHER_PORT="$CATCHER_PORT" CATCHER_MODE_FILE="$CATCHER_MODE_FILE" \
    python3 "$WORKDIR/catcher.py" &
  CATCHER_PID=$!
  for _ in $(seq 1 20); do
    curl -sf -o /dev/null "http://127.0.0.1:$CATCHER_PORT/" 2>/dev/null && break
    sleep 0.2
  done
}
start_catcher
touch "$CATCHER_LOG"

request_count() { wc -l < "$CATCHER_LOG" | tr -d ' '; }
reset_catcher_log() { : > "$CATCHER_LOG"; }

# --- deterministic fake checks, sharing the real contract: last stdout
# line's 2nd field is the severity word; exit code follows each real
# script's own convention (0=OK/WARN, 1=FAIL, 3=CRITICAL). --------------
cat > "$WORKDIR/fake-check.sh" <<'FAKEEOF'
#!/usr/bin/env bash
sev="$1"
ts="$(date -u +%Y-%m-%dT%H:%M:%SZ)"
case "$sev" in
  OK) echo "$ts OK detail=synthetic"; exit 0 ;;
  WARN) echo "$ts WARN detail=synthetic"; exit 0 ;;
  FAIL) echo "$ts FAIL reason=synthetic_fail"; exit 1 ;;
  CRITICAL) echo "$ts CRITICAL reason=synthetic_critical"; exit 3 ;;
esac
FAKEEOF
chmod +x "$WORKDIR/fake-check.sh"

export NTFY_BASE_URL="http://127.0.0.1:$CATCHER_PORT"
export NTFY_TOPIC="test-topic-$$"
export NOTIFY_STATE_DIR="$WORKDIR/notify-state"
export NOTIFY_LOG="$WORKDIR/notification.log"
export NOTIFY_ENV_FILE="$WORKDIR/does-not-exist.env" # prove no real secret file is required for this test
export NOTIFY_REMINDER_SECONDS=3600
mkdir -p "$NOTIFY_STATE_DIR"

wrap() { scripts/run-check-with-alert.sh "$@"; }

echo "=== Section 12/15: synthetic-send proves delivery reaches the channel ==="
wrap --synthetic-send demo-check TEST "synthetic delivery proof" >/dev/null
check "synthetic-send reached the disposable catcher" "$(request_count)" "1"
check "synthetic-send used the TEST tag" "$(command grep -c 'TAGS=test_tube' "$CATCHER_LOG")" "1"
reset_catcher_log

echo "=== Real-script integration: NATS FAIL (disposable invalid endpoint) sends exactly one ALERT ==="
set +e
NATS_MONITOR_URL="http://127.0.0.1:1" STATE_FILE="$WORKDIR/nats-check.log" \
  wrap nats-jetstream-real scripts/nats-jetstream-check.sh >/dev/null 2>&1
real_nats_exit=$?
set -e
check "wrapper preserves NATS check's non-zero exit on FAIL" "$( [ "$real_nats_exit" -ne 0 ] && echo nonzero || echo zero )" "nonzero"
check "exactly one ALERT reached the catcher for a real NATS failure" "$(request_count)" "1"
reset_catcher_log

echo "=== Real-script integration: WAHA FAIL (nonexistent container) sends exactly one ALERT ==="
set +e
WAHA_CONTAINER="omnira-waha-does-not-exist" WAHA_SESSION="unused" STATE_FILE="$WORKDIR/waha-check.log" \
  wrap waha-session-real scripts/waha-session-check.sh >/dev/null 2>&1
real_waha_exit=$?
set -e
check "wrapper preserves WAHA check's non-zero exit on FAIL" "$( [ "$real_waha_exit" -ne 0 ] && echo nonzero || echo zero )" "nonzero"
check "exactly one ALERT reached the catcher for a real WAHA failure" "$(request_count)" "1"
check "the real WAHA container/session were never contacted" "$(command grep -c "container_not_running" "$WORKDIR/waha-check.log")" "1"
reset_catcher_log

# --- fake-driven state machine: a fresh check name, starting from no
# state file at all (Section 15's "first occurrence" case). -------------
CHECK=fake-demo
run_fake() { NOTIFY_TEST_NOW_EPOCH="${2:-}" wrap "$CHECK" "$WORKDIR/fake-check.sh" "$1" >/dev/null 2>&1; }

BASE_EPOCH=$(date -u +%s)

echo "=== Section 15: first FAIL (no prior state) sends exactly one ALERT ==="
set +e; run_fake FAIL "$BASE_EPOCH"; e1=$?; set -e
check "wrapper exit is the check's own non-zero exit" "$e1" "1"
check "exactly one ALERT on first failure" "$(request_count)" "1"
check "state file records LAST_SEVERITY=FAIL" "$(command grep -c '^LAST_SEVERITY=FAIL$' "$NOTIFY_STATE_DIR/$CHECK.state")" "1"
reset_catcher_log

echo "=== Section 16: immediate repeat FAIL (same severity, no time elapsed) is suppressed ==="
set +e; run_fake FAIL "$BASE_EPOCH"; set -e
check "no additional ALERT for an immediate repeat of the SAME severity" "$(request_count)" "0"
check "notification log recorded a suppression" "$(command grep -c 'action=suppressed' "$NOTIFY_LOG")" "1"

echo "=== Section 17: reminder fires once >=60 minutes have passed (time-injected, no real sleep) ==="
LATER_EPOCH=$(( BASE_EPOCH + 3700 ))
set +e; run_fake FAIL "$LATER_EPOCH"; set -e
check "exactly one REMINDER after the interval elapses" "$(request_count)" "1"
check "the REMINDER used the hourglass tag" "$(command grep -c 'TAGS=hourglass' "$CATCHER_LOG")" "1"
reset_catcher_log

echo "=== Section 3: a severity CHANGE while still failing (FAIL -> CRITICAL) alerts immediately ==="
set +e; run_fake CRITICAL "$LATER_EPOCH"; set -e
check "an immediate ALERT fires on FAIL->CRITICAL despite being within the reminder window" "$(request_count)" "1"
check "state file now records CRITICAL" "$(command grep -c '^LAST_SEVERITY=CRITICAL$' "$NOTIFY_STATE_DIR/$CHECK.state")" "1"
reset_catcher_log

echo "=== Section 18: recovery on a real OK sends exactly one RECOVERY, then silence ==="
set +e; run_fake OK "$LATER_EPOCH"; e_ok=$?; set -e
check "wrapper exit is 0 on a healthy check with a successful RECOVERY send" "$e_ok" "0"
check "exactly one RECOVERY after healing" "$(request_count)" "1"
check "the RECOVERY used the white_check_mark tag" "$(command grep -c 'TAGS=white_check_mark' "$CATCHER_LOG")" "1"
reset_catcher_log
set +e; run_fake OK "$LATER_EPOCH"; set -e
check "a subsequent OK stays silent" "$(request_count)" "0"

echo "=== Section 19: WARN semantics never alert, and FAIL->WARN sends exactly one RECOVERY ==="
set +e; run_fake WARN "$LATER_EPOCH"; set -e
check "OK -> WARN sends nothing" "$(request_count)" "0"
set +e; run_fake WARN "$LATER_EPOCH"; set -e
check "WARN -> WARN sends nothing" "$(request_count)" "0"
set +e; run_fake FAIL "$LATER_EPOCH"; set -e
reset_catcher_log
set +e; run_fake WARN "$LATER_EPOCH"; set -e
check "FAIL -> WARN sends exactly one RECOVERY (cleared alerting failure, still WARN locally)" "$(request_count)" "1"
check "that RECOVERY used the white_check_mark tag" "$(command grep -c 'TAGS=white_check_mark' "$CATCHER_LOG")" "1"
reset_catcher_log

echo "=== Section 5: two concurrent runs of the SAME check never both send an initial ALERT ==="
CONC_CHECK=fake-concurrent
rm -f "$NOTIFY_STATE_DIR/$CONC_CHECK.state" "$NOTIFY_STATE_DIR/$CONC_CHECK.lock"
( NOTIFY_TEST_NOW_EPOCH="$BASE_EPOCH" wrap "$CONC_CHECK" "$WORKDIR/fake-check.sh" FAIL >/dev/null 2>&1 ) &
p1=$!
( NOTIFY_TEST_NOW_EPOCH="$BASE_EPOCH" wrap "$CONC_CHECK" "$WORKDIR/fake-check.sh" FAIL >/dev/null 2>&1 ) &
p2=$!
wait "$p1" "$p2" 2>/dev/null || true
check "exactly one ALERT sent across two overlapping first-failure runs" "$(request_count)" "1"
reset_catcher_log

echo "=== Section 20: notification backend down — check truth preserved, retry allowed next run ==="
DOWN_CHECK=fake-notifydown
rm -f "$NOTIFY_STATE_DIR/$DOWN_CHECK.state"
kill "$CATCHER_PID" 2>/dev/null || true
wait "$CATCHER_PID" 2>/dev/null || true
set +e
NOTIFY_TEST_NOW_EPOCH="$BASE_EPOCH" wrap "$DOWN_CHECK" "$WORKDIR/fake-check.sh" FAIL >/dev/null 2>&1
down_exit=$?
set -e
check "check's own FAIL exit code survives a dead notification backend" "$down_exit" "1"
check "notification failure was logged distinctly" "$(command grep -c 'action=notification_failed' "$NOTIFY_LOG")" "1"
check "no secret leaked into the notification log" "$(command grep -c "$NTFY_TOPIC" "$NOTIFY_LOG" || true)" "0"
check "LAST_NOTIFIED_AT was NOT advanced on a failed send (retry must not wait for the reminder window)" \
  "$(command grep '^LAST_NOTIFIED_AT=' "$NOTIFY_STATE_DIR/$DOWN_CHECK.state")" "LAST_NOTIFIED_AT=0"

start_catcher
for _ in $(seq 1 20); do curl -sf -o /dev/null "http://127.0.0.1:$CATCHER_PORT/" 2>/dev/null && break; sleep 0.2; done
reset_catcher_log
set +e
NOTIFY_TEST_NOW_EPOCH="$BASE_EPOCH" wrap "$DOWN_CHECK" "$WORKDIR/fake-check.sh" FAIL >/dev/null 2>&1
retry_exit=$?
set -e
check "the very next run retries the alert immediately (not suppressed) after a prior send failure" "$(request_count)" "1"
check "retry exit code still reflects the real check failure" "$retry_exit" "1"

echo "=== Section 8: check healthy but notification layer fails -> distinct exit 10 ==="
HEALTHYFAIL_CHECK=fake-healthy-notify-down
rm -f "$NOTIFY_STATE_DIR/$HEALTHYFAIL_CHECK.state"
# Seed a prior FAILING state so an OK run is expected to send a RECOVERY.
{
  echo "LAST_SEVERITY=FAIL"
  echo "LAST_REASON=seeded"
  echo "LAST_NOTIFIED_AT=0"
} > "$NOTIFY_STATE_DIR/$HEALTHYFAIL_CHECK.state"
kill "$CATCHER_PID" 2>/dev/null || true
wait "$CATCHER_PID" 2>/dev/null || true
set +e
NOTIFY_TEST_NOW_EPOCH="$BASE_EPOCH" wrap "$HEALTHYFAIL_CHECK" "$WORKDIR/fake-check.sh" OK >/dev/null 2>&1
healthy_notify_down_exit=$?
set -e
check "wrapper reports exit 10 when the check is healthy but the due RECOVERY could not be sent" "$healthy_notify_down_exit" "10"
start_catcher
for _ in $(seq 1 20); do curl -sf -o /dev/null "http://127.0.0.1:$CATCHER_PORT/" 2>/dev/null && break; sleep 0.2; done

echo "=== Section 25: the documented FUTURE NATS cron line does not narrow to a single consumer ==="
future_line=$(command grep -A2 'PILOT.4E2-FUTURE-CRON-NATS' docs/operations/PILOT-RUNBOOK.md | tail -1)
check "future documented cron line has no bare CONSUMER= override" \
  "$(printf '%s' "$future_line" | command grep -cE '(^|[[:space:]])CONSUMER=')" "0"
check "future documented cron line exists and is non-empty" "$( [ -n "$future_line" ] && echo yes || echo no )" "yes"

echo ""
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
