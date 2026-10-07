#!/bin/bash
# Guards web/nginx.conf against the stale-upstream trap (2026-10-07): the `web` nginx must keep reaching the api after the api
# container is REPLACED by one with a new IP, with no nginx reload (a literal `proxy_pass http://api:8080` resolves only at start
# and answered 502 on the Meta webhook). Throwaway docker network and containers; touches nothing of the real stack.
# Also checks that the proxied paths and query strings reach the api unchanged, and that /internal/ stays blocked.
# Usage: scripts/test-web-nginx-resolver.sh [path/to/nginx.conf]   (default: web/nginx.conf)
set -u
ROOT=$(cd "$(dirname "$0")/.." && pwd)
CONF=${1:-$ROOT/web/nginx.conf}
NET=ngxtest-$$; WEB=ngxweb-$$; API1=ngxapi1-$$; API2=ngxapi2-$$; fail=0
TMP=$(mktemp -d); trap 'docker rm -f $WEB $API1 $API2 >/dev/null 2>&1; docker network rm $NET >/dev/null 2>&1; rm -rf "$TMP"' EXIT
echo 'server { listen 8080; location / { return 200 "backend=$hostname uri=$request_uri\n"; } }' > "$TMP/backend.conf"
ip() { docker inspect -f '{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}' "$1"; }
startapi() { docker run -d --rm --name "$1" --hostname "$1" --network $NET --network-alias api -v "$TMP/backend.conf":/etc/nginx/conf.d/default.conf:ro nginx:alpine >/dev/null; }
docker network create $NET >/dev/null
startapi $API1; echo "api ip before: $(ip $API1)"
docker run -d --rm --name $WEB --network $NET -v "$CONF":/etc/nginx/conf.d/default.conf:ro \
  -v "$ROOT/web/security-headers.conf":/etc/nginx/snippets/security-headers.conf:ro nginx:alpine >/dev/null
sleep 2
req() { docker exec $WEB wget -q -O- --header 'Host: omnira.devops.k3gsolutions.com.br' "http://127.0.0.1$1" 2>&1 | head -1; }
chk() { out=$(req "$2"); if echo "$out" | grep -q "$3"; then echo "  OK   $1"; else echo "  FAIL $1 -> ${out:-<empty>} (wanted $3)"; fail=1; fi; }
echo "[before replacement]"
chk "/api/ keeps path and query"      "/api/v1/ping?x=1&y=2" "backend=$API1 uri=/api/v1/ping?x=1&y=2"
chk "Meta webhook keeps hub.* query"  "/webhooks/v1/whatsapp/meta?hub.mode=subscribe&hub.challenge=77" "backend=$API1 uri=/webhooks/v1/whatsapp/meta?hub.mode=subscribe&hub.challenge=77"
chk "SSE events path"                 "/api/v1/tenants/abc/inbox/events" "backend=$API1 uri=/api/v1/tenants/abc/inbox/events"
chk "SSE conversation events path"    "/api/v1/tenants/abc/inbox/conversations/c1/events" "backend=$API1 uri=/api/v1/tenants/abc/inbox/conversations/c1/events"
chk "/internal/ stays blocked"        "/internal/x" "404"
echo "[replace the api container (new one first, so it cannot reuse the old IP); NO nginx reload]"
startapi $API2; docker rm -f $API1 >/dev/null; sleep 7
echo "api ip after: $(ip $API2)"
chk "/api/ after replacement"         "/api/v1/ping?x=1" "backend=$API2 uri=/api/v1/ping?x=1"
chk "Meta webhook after replacement"  "/webhooks/v1/whatsapp/meta?hub.challenge=9" "backend=$API2 uri=/webhooks/v1/whatsapp/meta?hub.challenge=9"
chk "SSE after replacement"           "/api/v1/tenants/abc/inbox/events" "backend=$API2 uri=/api/v1/tenants/abc/inbox/events"
[ $fail -eq 0 ] && echo "PASS" || echo "FAIL"
exit $fail
