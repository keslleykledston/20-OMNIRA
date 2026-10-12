#!/usr/bin/env bash
# Proof that the web image's nginx streams the Hub change signal without buffering (ADR-0040 §25). A stub upstream named `api`
# answers an SSE route with one event at once and a second one 3 s later; the first must reach the client in well under a second.
# The client asks for gzip like a browser does: the signal must still come at once and uncompressed (a compressed stream would be held back).
# (Plain proxy buffering of a small chunk is NOT held by nginx, so the old "buffered control" idea cannot discriminate; the real hazards are gzip, a missing location and a wrong upstream.)
# Usage: scripts/test-web-nginx-sse.sh [web-image]   (default: the image built from the working tree, not the running one)
set -euo pipefail
cd "$(dirname "$0")/.."
IMG="${1:-}"
RUN=nginxsse-$$
NET=omnira-$RUN-net
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")
cleanup() { docker ps -aq --filter "name=$RUN" | xargs -r docker rm -fvv >/dev/null 2>&1 || true; docker network rm "$NET" >/dev/null 2>&1 || true; }
trap cleanup EXIT
if [ -z "$IMG" ]; then IMG="omnira-$RUN-web:test"; docker build -q -t "$IMG" ./web >/dev/null; fi
docker network create "${LBL[@]}" "$NET" >/dev/null

STUB=$(mktemp -d); chmod 755 "$STUB"
cat > "$STUB/stub.py" <<'PY'
import time
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
class H(BaseHTTPRequestHandler):
    protocol_version = "HTTP/1.1"
    def do_GET(self):
        self.send_response(200)
        self.send_header("Content-Type", "text/event-stream")
        self.send_header("Transfer-Encoding", "chunked")
        self.end_headers()
        for i in range(2):
            body = b"event: changed\ndata: {}\n\n"
            self.wfile.write(b"%x\r\n%s\r\n" % (len(body), body)); self.wfile.flush()
            if i == 0: time.sleep(3)
        self.wfile.write(b"0\r\n\r\n"); self.wfile.flush()
    def log_message(self, *a): pass
ThreadingHTTPServer(("0.0.0.0", 8080), H).serve_forever()
PY
docker run -d --name "$RUN-api" --network "$NET" --network-alias api "${LBL[@]}" -v "$STUB:/s:ro" python:3-alpine python /s/stub.py >/dev/null
docker run -d --name "$RUN-web" --network "$NET" "${LBL[@]}" "$IMG" >/dev/null
for i in $(seq 1 40); do docker exec "$RUN-web" wget -q -O /dev/null http://127.0.0.1/ 2>/dev/null && break; sleep 0.5; done

WEBIP=$(docker inspect -f '{{(index .NetworkSettings.Networks "'"$NET"'").IPAddress}}' "$RUN-web")
ms_of_lines() { # path: "<ms of 1st event line> <ms of 2nd event line> <content-encoding>" as the client sees them
  local start t1 t2
  start=$(date +%s%N)
  curl -sN --max-time 8 -H 'Accept-Encoding: gzip' -D "$STUB/h" "http://$WEBIP$1" | {
    read -r _a; t1=$(date +%s%N); read -r _b; read -r _c; read -r _d; t2=$(date +%s%N)
    echo "$(( (t1 - start) / 1000000 )) $(( (t2 - start) / 1000000 ))"; cat >/dev/null; }
}
HUB="/api/v1/hubs/11111111-2222-3333-4444-555555555555/events"
read -r first second < <(ms_of_lines "$HUB")
enc=$(grep -i '^content-encoding' "$STUB/h" || true)
echo "Hub signal: 1st event at ${first} ms, 2nd at ${second} ms (stub sends the 2nd 3 s after the 1st); encoding header: '${enc:-none}'"
[ "$first" -lt 1000 ] || { echo "FAIL: the first event was held by nginx"; exit 1; }
[ "$second" -ge 2500 ] && [ "$second" -lt 6000 ] || { echo "FAIL: the second event did not arrive about 3 s after the first (the stream was batched or cut)"; exit 1; }
[ -z "$enc" ] || { echo "FAIL: the signal was compressed"; exit 1; }
echo "PASS: nginx streams the Hub change signal event by event, uncompressed"
