#!/usr/bin/env bash
# Disposable proof for scripts/backup-cloud-check.sh and its use under
# scripts/run-check-with-alert.sh. A local Python http.server stands in for
# ntfy (127.0.0.1 only); no real topic, network, Google account or live
# backup directory is touched.
#
# Usage: scripts/test-backup-cloud-check.sh
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

BK="$WORKDIR/bk"; mkdir -p "$BK"
CONF="$WORKDIR/rclone-backup.conf"
MISSING="$WORKDIR/none.conf"
STATE="$WORKDIR/check.log"
printf '[omnira-backup]\ntype = local\n' > "$CONF"
export BACKUP_DIR="$BK" STATE_FILE="$STATE" BACKUP_CLOUD_REMOTE=omnira-backup

BASE=1790000000
run() { # run <conf> <now-epoch>; sets out/code
  set +e
  out=$(BACKUP_CLOUD_CONF="$1" CHECK_NOW_EPOCH="$2" scripts/backup-cloud-check.sh 2>&1)
  code=$?
  set -e
}
last() { printf '%s\n' "$out" | tail -1 | awk '{print $2}'; }
reason() { printf '%s\n' "$out" | tail -1 | grep -oE 'reason=[^ ]+'; }

echo "=== Section 1: not configured ==="
run "$MISSING" "$BASE"
check "no config, no marker => WARN" "$(last)" "WARN"
check "no config, no marker => exit 0" "$code" "0"
check "reason is cloud_not_configured" "$(reason)" "reason=cloud_not_configured"
touch -d "@$BASE" "$BK/.cloud-last-ok"
run "$MISSING" "$BASE"
check "config vanished after a past success => FAIL" "$(last)" "FAIL"
check "config vanished => exit 1" "$code" "1"
check "reason is cloud_config_missing_after_success" "$(reason)" "reason=cloud_config_missing_after_success"

echo "=== Section 2: freshness thresholds with a configured remote ==="
touch -d "@$BASE" "$BK/.cloud-last-ok"
run "$CONF" $((BASE + 600))
check "10 min old => OK" "$(last)" "OK"
check "10 min old => exit 0" "$code" "0"
run "$CONF" $((BASE + 4 * 3600))
check "4 h old => WARN" "$(last)" "WARN"
check "4 h old => exit 0 (WARN never fails the wrapper)" "$code" "0"
check "4 h old reason" "$(reason)" "reason=cloud_backup_aging"
run "$CONF" $((BASE + 7 * 3600))
check "7 h old => FAIL" "$(last)" "FAIL"
check "7 h old => exit 1" "$code" "1"
check "7 h old reason" "$(reason)" "reason=cloud_backup_stale"

echo "=== Section 3: configured but never succeeded ==="
rm -f "$BK/.cloud-last-ok"
touch -d "@$BASE" "$CONF"
run "$CONF" $((BASE + 3600))
check "1 h after setup => WARN awaiting first success" "$(reason)" "reason=cloud_awaiting_first_success"
check "1 h after setup => exit 0" "$code" "0"
run "$CONF" $((BASE + 7 * 3600))
check "7 h after setup, still none => FAIL" "$(last)" "FAIL"
check "never-succeeded reason" "$(reason)" "reason=cloud_never_succeeded"

echo "=== Section 4: every run is logged to STATE_FILE ==="
check "state file has one line per run" "$(grep -c ' reason=' "$STATE")" "7"

echo "=== Section 5: under the ntfy wrapper (disposable catcher) ==="
CATCHER_LOG="$WORKDIR/catcher.log"; : > "$CATCHER_LOG"
CATCHER_PORT=$(( (RANDOM % 20000) + 20000 ))
cat > "$WORKDIR/catcher.py" <<'PYEOF'
import http.server, json, os
class H(http.server.BaseHTTPRequestHandler):
    def do_POST(self):
        body = self.rfile.read(int(self.headers.get("Content-Length", 0))).decode("utf-8", "replace")
        try: t = json.loads(body).get("title", "")
        except Exception: t = ""
        with open(os.environ["CATCHER_LOG"], "a") as f: f.write(t + "\n")
        self.send_response(200); self.send_header("Content-Length", "2"); self.end_headers(); self.wfile.write(b"ok")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", int(os.environ["CATCHER_PORT"])), H).serve_forever()
PYEOF
CATCHER_LOG="$CATCHER_LOG" CATCHER_PORT="$CATCHER_PORT" python3 "$WORKDIR/catcher.py" &
CATCHER_PID=$!
for _ in $(seq 1 25); do curl -s -o /dev/null "http://127.0.0.1:$CATCHER_PORT/" 2>/dev/null && break; sleep 0.2; done

export NOTIFY_ENV_FILE=/nonexistent NOTIFY_STATE_DIR="$WORKDIR/nstate" NOTIFY_LOG="$WORKDIR/notification.log"
export NTFY_BASE_URL="http://127.0.0.1:$CATCHER_PORT" NTFY_TOPIC="test-topic-$$"
export BACKUP_CLOUD_CONF="$CONF"
unset CHECK_NOW_EPOCH
wrap() { scripts/run-check-with-alert.sh backup-cloud scripts/backup-cloud-check.sh >/dev/null 2>&1; }
alerts() { wc -l < "$CATCHER_LOG" | tr -d ' '; }

touch -d "7 hours ago" "$BK/.cloud-last-ok"
set +e; wrap; w1=$?; set -e
check "stale marker: wrapper preserves exit 1" "$w1" "1"
check "stale marker: exactly one ALERT sent" "$(alerts)" "1"
check "the ALERT names the check" "$(grep -c 'backup-cloud' "$CATCHER_LOG")" "1"
set +e; wrap; set -e
check "repeat while still failing is suppressed" "$(alerts)" "1"
touch "$BK/.cloud-last-ok"
set +e; wrap; w3=$?; set -e
check "fresh marker: wrapper exits 0" "$w3" "0"
check "fresh marker: one RECOVERY sent" "$(alerts)" "2"
check "second message is a RECOVERY" "$(tail -1 "$CATCHER_LOG" | grep -c RECOVERY)" "1"
check "topic never leaks into the notification log" "$(grep -c "$NTFY_TOPIC" "$NOTIFY_LOG" || true)" "0"

echo "=== Section 6: aging (WARN) stays silent externally ==="
touch -d "4 hours ago" "$BK/.cloud-last-ok"
before=$(alerts)
set +e; wrap; w6=$?; set -e
check "WARN: wrapper exits 0" "$w6" "0"
check "WARN: no notification" "$(alerts)" "$before"

bash -n scripts/backup-cloud-check.sh
echo ""
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
