#!/usr/bin/env bash
# W3 — real WhatsApp smoke (WAHA): pair a phone, receive in the Inbox, reply, watch acks.
# Automates infra + API calls; pauses only for the steps a human must do.
#   scripts/w3-smoke.sh              full interactive run (needs a phone with WhatsApp)
#   scripts/w3-smoke.sh --until-qr   automated part only (infra up, session reaches QR), then cleanup
#   scripts/w3-smoke.sh --keep       do not tear down at the end
# Requires: docker, curl, python3; running containers omnira-postgres (55434) and omnira-nats (4222).
# Uses a throwaway DB (omnira_w3), a throwaway WAHA container and local API/worker containers.
set -uo pipefail
cd "$(dirname "$0")/.."

UNTIL_QR=0; KEEP=0
for a in "$@"; do case "$a" in --until-qr) UNTIL_QR=1;; --keep) KEEP=1;; *) echo "unknown flag $a"; exit 2;; esac; done

DB=omnira_w3; WAHA_PORT=23100; API_PORT=28940; WAHA_KEY=w3key
TENANT=11111111-1111-1111-1111-111111111111
API=http://127.0.0.1:$API_PORT/api/v1
QR_FILE=${TMPDIR:-/tmp}/omnira-w3-qr.png
PSQL_OWNER=(docker exec -i omnira-postgres psql -U omnira -v ON_ERROR_STOP=1 -q)
BLD=$'\033[1m'; GRN=$'\033[32m'; RED=$'\033[31m'; YEL=$'\033[33m'; NC=$'\033[0m'
PASS=(); FAIL=()
say()  { printf '%s\n' "${BLD}$*${NC}"; }
ok()   { PASS+=("$*"); printf '%s\n' "  ${GRN}✓${NC} $*"; }
bad()  { FAIL+=("$*"); printf '%s\n' "  ${RED}✗${NC} $*"; }
die()  { printf '%s\n' "${RED}$*${NC}" >&2; cleanup; exit 1; }
pause(){ [ "$UNTIL_QR" = 1 ] && return; read -r -p "  ${YEL}▶ $* [Enter]${NC} " _; }
askyn(){ [ "$UNTIL_QR" = 1 ] && return 0; local a; read -r -p "  ${YEL}? $* [y/N]${NC} " a; [[ "$a" =~ ^[yY] ]]; }

cleanup() {
  [ "$KEEP" = 1 ] && { say "--keep: leaving containers and DB $DB running"; return; }
  docker rm -f omnira-w3-api omnira-w3-worker omnira-w3-waha >/dev/null 2>&1
  "${PSQL_OWNER[@]}" -d postgres -c "DROP DATABASE IF EXISTS $DB" >/dev/null 2>&1
}
trap 'cleanup' EXIT

json() { python3 -c "import sys,json;d=json.loads(sys.stdin.read().rsplit('\n[',1)[0]);print($1)"; }
call() { # call TOKEN curl-args... -> body + "\n[code]"
  local tok=$1; shift
  curl -s -w '\n[%{http_code}]' -H "Authorization: Bearer $tok" -H 'Content-Type: application/json' "$@"
}
code() { sed -n 's/^\[\([0-9]*\)\]$/\1/p' | tail -1; }
login() { curl -s -X POST "$API/auth/login" -H 'Content-Type: application/json' -d "{\"email\":\"$1\"}" | python3 -c 'import sys,json;print(json.load(sys.stdin)["token"])'; }

say "0/6 Pre-flight"
for c in docker curl python3; do command -v $c >/dev/null || die "missing $c"; done
docker ps --format '{{.Names}}' | grep -qx omnira-postgres || die "container omnira-postgres is not running"
docker ps --format '{{.Names}}' | grep -qx omnira-nats || die "container omnira-nats is not running"
docker image inspect devlikeapro/waha:gows-2026.8.2 >/dev/null 2>&1 || docker pull -q devlikeapro/waha:gows-2026.8.2 >/dev/null || die "cannot get WAHA image"
ok "docker, postgres, nats, WAHA image"

say "1/6 Database, images, containers"
"${PSQL_OWNER[@]}" -d postgres -c "DROP DATABASE IF EXISTS $DB" -c "CREATE DATABASE $DB" >/dev/null 2>&1 || die "cannot create $DB"
for f in migrations/*.up.sql; do "${PSQL_OWNER[@]}" -d $DB < "$f" >/dev/null 2>/tmp/w3.err || { cat /tmp/w3.err; die "migration failed: $f"; }; done
"${PSQL_OWNER[@]}" -d $DB <<SQL >/dev/null || die "seed failed"
INSERT INTO users(id,external_subject,email,status) VALUES
 ('22222222-2222-2222-2222-222222222222','test@omnira.local','test@omnira.local','active'),
 ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa','admin@omnira.local','admin@omnira.local','active');
INSERT INTO tenants(id,legal_name,status) VALUES ('$TENANT','W3 Smoke','active');
INSERT INTO memberships(tenant_id,user_id,role_id,status)
 SELECT '$TENANT','22222222-2222-2222-2222-222222222222',id,'active' FROM roles WHERE key='tenant_agent' AND tenant_id IS NULL;
INSERT INTO memberships(tenant_id,user_id,role_id,status)
 SELECT '$TENANT','aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa',id,'active' FROM roles WHERE key='tenant_admin' AND tenant_id IS NULL;
INSERT INTO queues(tenant_id,name,mode,is_default) VALUES ('$TENANT','Default','manual',true);
SQL
docker build -q -f Dockerfile.api -t omnira-api:w3 . >/dev/null || die "api build failed"
docker build -q -f Dockerfile.worker -t omnira-worker:w3 . >/dev/null || die "worker build failed"
docker rm -f omnira-w3-api omnira-w3-worker omnira-w3-waha >/dev/null 2>&1
docker run -d --name omnira-w3-waha --add-host=host.docker.internal:host-gateway -p 127.0.0.1:$WAHA_PORT:3000 \
  -e WAHA_API_KEY=$WAHA_KEY -e WHATSAPP_DEFAULT_ENGINE=GOWS -e WAHA_DASHBOARD_ENABLED=false -e WHATSAPP_SWAGGER_ENABLED=false \
  devlikeapro/waha:gows-2026.8.2 >/dev/null || die "cannot start WAHA"
KEY=$(head -c 32 /dev/urandom | base64)
ENVS=(-e OMNIRA_CREDENTIALS_KEY="$KEY" -e OMNIRA_WAHA_ENABLED=true -e OMNIRA_WAHA_BASE_URL=http://127.0.0.1:$WAHA_PORT -e OMNIRA_WAHA_API_KEY=$WAHA_KEY
      -e OMNIRA_DATABASE_URL="postgres://omnira:omnira@127.0.0.1:55434/$DB?sslmode=disable&options=-c%20role%3Domnira_app" -e OMNIRA_NATS_URL=nats://127.0.0.1:4222)
# WAHA runs in a bridge network and must reach the API on the host to deliver webhooks.
docker run -d --name omnira-w3-api --network host -e OMNIRA_HTTP_ADDR=0.0.0.0:$API_PORT -e OMNIRA_PUBLIC_BASE_URL=http://host.docker.internal:$API_PORT "${ENVS[@]}" omnira-api:w3 >/dev/null || die "cannot start API"
docker run -d --name omnira-w3-worker --network host "${ENVS[@]}" omnira-worker:w3 >/dev/null || die "cannot start worker"
for i in $(seq 1 30); do curl -sf -o /dev/null -H "X-Api-Key: $WAHA_KEY" localhost:$WAHA_PORT/health && break; sleep 1; done
curl -sf -o /dev/null -H "X-Api-Key: $WAHA_KEY" localhost:$WAHA_PORT/health && ok "WAHA healthy" || die "WAHA not healthy"
for i in $(seq 1 20); do curl -s -o /dev/null "$API/auth/health" && break; sleep 1; done
ADM=$(login admin@omnira.local) && AG=$(login test@omnira.local) && ok "API up, logins ok" || die "API login failed"

say "2/6 Connection + session"
R=$(call "$ADM" -X POST "$API/tenants/$TENANT/channels/waha/connections" -d '{"risk_acknowledged":true}')
[ "$(echo "$R" | code)" = 201 ] && ok "connection created (risk acknowledged)" || die "create connection failed: $R"
CID=$(echo "$R" | json 'd["id"]')
R=$(call "$ADM" -X POST "$API/tenants/$TENANT/channels/waha/connections/$CID/session/start")
[ "$(echo "$R" | code)" = 200 ] && ok "session start accepted" || die "start failed: $R"
say "   waiting for QR..."
GOT=0
for i in $(seq 1 45); do
  R=$(call "$ADM" "$API/tenants/$TENANT/channels/waha/connections/$CID/qr")
  if [ "$(echo "$R" | code)" = 200 ]; then echo "$R" | json 'd["data"]' | base64 -d > "$QR_FILE" 2>/dev/null && GOT=1; break; fi
  sleep 2
done
if [ "$GOT" = 1 ] && head -c 4 "$QR_FILE" | grep -q PNG; then ok "QR saved to $QR_FILE"; else bad "no valid QR PNG produced"; fi
[ "$UNTIL_QR" = 1 ] && { say "--until-qr: automated part finished"; printf '\n%s\n' "${GRN}PASS ${#PASS[@]}${NC}  ${RED}FAIL ${#FAIL[@]}${NC}"; [ ${#FAIL[@]} = 0 ]; exit $?; }

say "3/6 Pair the phone (HUMAN)"
echo "  Open $QR_FILE on a screen (e.g. xdg-open), then on the phone:"
echo "  WhatsApp → Settings → Linked devices → Link a device → scan the QR."
echo "  ${YEL}Use a disposable number: unofficial WhatsApp automation can get numbers banned.${NC}"
pause "scan the QR now"
ACTIVE=0
for i in $(seq 1 60); do
  R=$(call "$ADM" "$API/tenants/$TENANT/channels/waha/connections/$CID")
  S=$(echo "$R" | json 'd["status"]'); [ "$S" = active ] && { ACTIVE=1; break; }; sleep 3
done
if [ $ACTIVE = 1 ]; then ok "connection active, paired account: $(echo "$R" | json 'd.get("external_account_id","?")')"; else bad "connection did not become active (status=$S)"; die "cannot continue without a paired session"; fi

say "4/6 Inbound (HUMAN)"
echo "  From ANOTHER phone, send a WhatsApp message to the paired number."
pause "message sent"
CONV=""
for i in $(seq 1 40); do
  R=$(call "$AG" "$API/tenants/$TENANT/inbox/conversations")
  CONV=$(echo "$R" | json '(d["items"][0]["id"] if d.get("items") else "")')
  [ -n "$CONV" ] && break; sleep 3
done
if [ -n "$CONV" ]; then ok "inbound created conversation $CONV"; else bad "no conversation appeared (check webhook reachability from the WAHA container)"; die "inbound failed"; fi
R=$(call "$AG" "$API/tenants/$TENANT/inbox/conversations/$CONV/messages")
echo "$R" | json 'any(m["direction"]=="inbound" for m in d["items"])' | grep -q True && ok "inbound message persisted" || bad "inbound message missing"

say "5/6 Reply through the API"
R=$(call "$AG" -X POST "$API/tenants/$TENANT/inbox/conversations/$CONV/assign")
[ "$(echo "$R" | code)" = 200 ] && ok "agent claimed the conversation" || bad "claim failed: $R"
R=$(call "$AG" -X POST "$API/tenants/$TENANT/inbox/conversations/$CONV/messages" -H "Idempotency-Key: w3-$(date +%s)-$RANDOM" -d '{"text":"Resposta de teste do OMNIRA ✅"}')
[ "$(echo "$R" | code)" = 202 ] && ok "reply queued (202)" || { bad "send failed: $R"; die "send failed"; }
MID=$(echo "$R" | json 'd["id"]')
LAST=""; SEEN=""
for i in $(seq 1 40); do
  ST=$("${PSQL_OWNER[@]}" -d $DB -tA -c "select status||':'||failure_reason from messages where id='$MID'")
  [ "$ST" != "$LAST" ] && { SEEN="$SEEN $ST"; LAST=$ST; printf '   status → %s\n' "$ST"; }
  case "$ST" in read:*) break;; failed:*) break;; esac; sleep 3
done
case "$SEEN" in *sent*) ok "worker sent it through WAHA";; *) bad "message never reached 'sent' (seen:$SEEN)";; esac
case "$SEEN" in *delivered*|*read*) ok "delivery ack applied from the webhook";; *) bad "no delivery ack observed (seen:$SEEN)";; esac
askyn "Did the reply arrive on the phone?" && ok "reply confirmed by a human" || bad "reply not confirmed on the phone"

say "6/6 Summary"
printf '  %s passed, %s failed\n' "${#PASS[@]}" "${#FAIL[@]}"
for f in "${FAIL[@]}"; do printf '  %s\n' "${RED}✗ $f${NC}"; done
[ ${#FAIL[@]} = 0 ]
