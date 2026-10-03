#!/usr/bin/env bash
# Disposable proof for scripts/setup-deadman-healthchecks.sh against a local mock
# of the healthchecks.io API (127.0.0.1 only). No account, key or network used.
#
# Usage: scripts/test-setup-deadman-healthchecks.sh
set -euo pipefail
cd "$(dirname "$0")/.."

WORKDIR=$(mktemp -d)
trap 'kill "${MOCK_PID:-0}" 2>/dev/null || true; rm -rf "$WORKDIR"' EXIT

pass=0
fail=0
check() {
  local desc="$1" got="$2" want="$3"
  if [ "$got" = "$want" ]; then echo "PASS: $desc"; pass=$((pass + 1)); else echo "FAIL: $desc (got '$got', want '$want')"; fail=$((fail + 1)); fi
}

PORT=$(( (RANDOM % 20000) + 20000 ))
GOOD_KEY="rw-key-$$"
cat > "$WORKDIR/mock.py" <<'PYEOF'
import http.server, json, os
LOG = os.environ["MOCK_LOG"]; PORT = int(os.environ["MOCK_PORT"]); GOOD = os.environ["GOOD_KEY"]
CHECKS = {}
class H(http.server.BaseHTTPRequestHandler):
    def _send(self, code, body):
        data = json.dumps(body).encode(); self.send_response(code); self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data))); self.end_headers(); self.wfile.write(data)
    def do_POST(self):
        n = int(self.headers.get("Content-Length", 0)); body = json.loads(self.rfile.read(n) or b"{}")
        with open(LOG, "a") as f: f.write("POST %s key=%s unique=%s timeout=%s grace=%s channels=%s\n" % (self.path, self.headers.get("X-Api-Key"), body.get("unique"), body.get("timeout"), body.get("grace"), body.get("channels")))
        if self.headers.get("X-Api-Key") != GOOD: return self._send(401, {"error": "wrong api key"})
        name = body["name"]
        if name in CHECKS: return self._send(200, CHECKS[name])
        uid = "11111111-2222-3333-4444-%012d" % (len(CHECKS) + 1)
        CHECKS[name] = {"name": name, "ping_url": "http://127.0.0.1:%d/ping/%s" % (PORT, uid)}
        self._send(201, CHECKS[name])
    def do_GET(self):
        with open(LOG, "a") as f: f.write("GET %s\n" % self.path)
        self.send_response(200); self.send_header("Content-Length", "2"); self.end_headers(); self.wfile.write(b"OK")
    def log_message(self, *a): pass
http.server.HTTPServer(("127.0.0.1", PORT), H).serve_forever()
PYEOF
MOCK_LOG="$WORKDIR/mock.log" MOCK_PORT=$PORT GOOD_KEY=$GOOD_KEY python3 "$WORKDIR/mock.py" &
MOCK_PID=$!
for _ in $(seq 1 25); do curl -s -o /dev/null "http://127.0.0.1:$PORT/" 2>/dev/null && break; sleep 0.2; done
: > "$WORKDIR/mock.log"

KEYFILE="$WORKDIR/healthchecks.env"; ENVFILE="$WORKDIR/notify.env"
printf 'HC_API_KEY=%s\n' "$GOOD_KEY" > "$KEYFILE"; chmod 600 "$KEYFILE"
printf 'NTFY_TOPIC=meu-topico\nNOTIFY_FOO=bar\nDEADMAN_URL="http://old.example/stale"\n' > "$ENVFILE"; chmod 644 "$ENVFILE"

# fresh logs so deadman-ping.sh has something to evaluate
export DEADMAN_LOG_DIR="$WORKDIR/logs" DEADMAN_BACKUP_LOG="$WORKDIR/backup.log" DEADMAN_LOG="$WORKDIR/deadman.log"
mkdir -p "$DEADMAN_LOG_DIR"
for f in "$DEADMAN_LOG_DIR/nats-jetstream-check.log" "$DEADMAN_LOG_DIR/waha-session-check.log" "$DEADMAN_LOG_DIR/backup-cloud-check.log" "$DEADMAN_BACKUP_LOG"; do : > "$f"; done

run() { set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$KEYFILE" --env-file "$ENVFILE" --api-url "http://127.0.0.1:$PORT" "$@" 2>&1); code=$?; set -e; }

echo "=== Section 1: first run creates the check and wires the env file ==="
run
check "exit 0" "$code" "0"
check "reports a creation" "$(printf '%s' "$out" | grep -c 'check created')" "1"
check "API got the key in the header" "$(grep -c "key=$GOOD_KEY" "$WORKDIR/mock.log")" "1"
check "request is idempotent by name (unique=['name'])" "$(grep -c "unique=\['name'\]" "$WORKDIR/mock.log")" "1"
check "period 5 min and grace 10 min" "$(grep -c 'timeout=300 grace=600' "$WORKDIR/mock.log")" "1"
check "all channels attached" "$(grep -c "channels=\*" "$WORKDIR/mock.log")" "1"
check "DEADMAN_URL is the new ping URL" "$(grep -c '^DEADMAN_URL="http://127.0.0.1:'$PORT'/ping/11111111-2222-3333-4444-000000000001"$' "$ENVFILE")" "1"
check "DEADMAN_FAIL_URL is ping URL + /fail" "$(grep -c '^DEADMAN_FAIL_URL=".*/ping/.*/fail"$' "$ENVFILE")" "1"
check "stale old URL was replaced, not duplicated" "$(grep -c '^DEADMAN_URL=' "$ENVFILE")" "1"
check "other settings were preserved" "$(grep -c -E '^(NTFY_TOPIC=meu-topico|NOTIFY_FOO=bar)$' "$ENVFILE")" "2"
check "env file mode is 0600" "$(stat -c %a "$ENVFILE")" "600"
check "first heartbeat was sent to the new URL" "$(grep -c '^GET /ping/11111111-2222-3333-4444-000000000001$' "$WORKDIR/mock.log")" "1"
check "deadman log says state=ok" "$(grep -c 'state=ok' "$DEADMAN_LOG")" "1"

echo "=== Section 2: nothing secret is printed ==="
check "API key not in the output" "$(printf '%s' "$out" | grep -c "$GOOD_KEY" || true)" "0"
check "ping URL not in the output" "$(printf '%s' "$out" | grep -c '/ping/' || true)" "0"
check "ping URL not in the deadman log" "$(grep -c '/ping/' "$DEADMAN_LOG" || true)" "0"

echo "=== Section 3: re-running reuses the check and keeps one pair of lines ==="
run --no-ping
check "exit 0" "$code" "0"
check "reports reuse" "$(printf '%s' "$out" | grep -c 'already existed')" "1"
check "still exactly one DEADMAN_URL" "$(grep -c '^DEADMAN_URL=' "$ENVFILE")" "1"
check "still exactly one DEADMAN_FAIL_URL" "$(grep -c '^DEADMAN_FAIL_URL=' "$ENVFILE")" "1"
check "same ping URL as before" "$(grep -c '^DEADMAN_URL=.*000000000001"$' "$ENVFILE")" "1"
check "fail URL points at the same check" "$(grep -c '^DEADMAN_FAIL_URL=.*000000000001/fail"$' "$ENVFILE")" "1"

echo "=== Section 3b: works when the directory is read-only (root-owned /etc/omnira) ==="
RODIR="$WORKDIR/rodir"; mkdir -p "$RODIR"; printf 'NTFY_TOPIC=keep-me\n' > "$RODIR/notify.env"; chmod 600 "$RODIR/notify.env"; chmod 555 "$RODIR"
set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$KEYFILE" --env-file "$RODIR/notify.env" --api-url "http://127.0.0.1:$PORT" --no-ping 2>&1); code=$?; set -e
chmod 755 "$RODIR"
check "read-only directory: exit 0" "$code" "0"
check "read-only directory: URL written" "$(grep -c '^DEADMAN_URL=' "$RODIR/notify.env")" "1"
check "read-only directory: other line kept" "$(grep -c '^NTFY_TOPIC=keep-me$' "$RODIR/notify.env")" "1"
check "read-only directory: mode stays 0600" "$(stat -c %a "$RODIR/notify.env")" "600"
check "no temp files left next to the env file" "$(ls "$RODIR" | grep -vc '^notify.env$' || true)" "0"

echo "=== Section 4: failures are explicit and change nothing ==="
before=$(cksum < "$ENVFILE")
printf 'HC_API_KEY=wrong-key\n' > "$WORKDIR/bad.env"
set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$WORKDIR/bad.env" --env-file "$ENVFILE" --api-url "http://127.0.0.1:$PORT" --no-ping 2>&1); code=$?; set -e
check "wrong key => exit 1" "$code" "1"
check "wrong key => explains read-write key" "$(printf '%s' "$out" | grep -c 'READ-WRITE')" "1"
check "env file untouched after a failure" "$(cksum < "$ENVFILE")" "$before"
set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$WORKDIR/nope.env" --env-file "$ENVFILE" --api-url "http://127.0.0.1:$PORT" 2>&1); code=$?; set -e
check "missing key file => exit 2" "$code" "2"
printf 'HC_API_KEY=\n' > "$WORKDIR/empty.env"
set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$WORKDIR/empty.env" --env-file "$ENVFILE" --api-url "http://127.0.0.1:$PORT" 2>&1); code=$?; set -e
check "empty key => exit 2" "$code" "2"
set +e; out=$(scripts/setup-deadman-healthchecks.sh --key-file "$KEYFILE" --env-file "$ENVFILE" --api-url "http://127.0.0.1:1" --no-ping 2>&1); code=$?; set -e
check "unreachable API => exit 1" "$code" "1"

bash -n scripts/setup-deadman-healthchecks.sh
echo ""
echo "== $pass passed, $fail failed =="
[ "$fail" -eq 0 ]
