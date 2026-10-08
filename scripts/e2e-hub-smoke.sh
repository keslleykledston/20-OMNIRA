#!/usr/bin/env bash
# End-to-end smoke of the Service Hub with the REAL binaries: omnira-api (dev login), omnira-hubctl (provisioning + projection)
# against a throwaway Postgres (production migrator) and NATS. Nothing here touches omnira_dev or any real service.
#
# Proves what the unit/integration suites cannot: the wiring in main.go (flag, middleware, authentication), the operator tool,
# the projector and the HTTP contract working together, then revocation taking effect and the flag turning the surface off.
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=hubsmoke-$$
PG=omnira-$RUN-pg; NATS=omnira-$RUN-nats
WORK=$(mktemp -d); chmod 777 "$WORK"
cleanup() { docker ps -aq --filter "name=$RUN" | xargs -r docker rm -f >/dev/null 2>&1 || true; rm -rf "$WORK" 2>/dev/null || true; }
APIPORT=$((18000 + $$ % 900))  # unique per run: the containers share the host network and a stale API must never answer for us
trap cleanup EXIT
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")

docker run -d --name "$PG" "${LBL[@]}" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=hubsmoke -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
docker run -d --name "$NATS" "${LBL[@]}" -p 127.0.0.1::4222 nats:2.10-alpine -js >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PGPORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2); NATSPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2)
psql_o() { docker exec -i "$PG" psql -U omnira -d hubsmoke -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== migrations (production migrator)"
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=hubsmoke postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1

echo "== seed: 3 companies with one conversation each; the dev-login user is the agent, another dev user has no hub"
AGENT=22222222-2222-2222-2222-222222222222; OTHER=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
TA=a0000000-0000-0000-0000-00000000000a; TB=b0000000-0000-0000-0000-00000000000b; TC=c0000000-0000-0000-0000-00000000000c
psql_o <<SQL
INSERT INTO users (id, external_subject, email) VALUES ('$AGENT','e2e-agent','test@omnira.local'), ('$OTHER','e2e-other','admin@omnira.local');
INSERT INTO tenants (id, legal_name, trade_name, status) VALUES ('$TA','ISP Roraima Ltda','ISP Roraima','active'), ('$TB','NorteNet Telecom','NorteNet','active'), ('$TC','Terceira Empresa','','active');
SQL
for t in "$TA:Jose Carlos:+5592911110001" "$TB:Maria Souza:+5592911110002" "$TC:Ana Lima:+5592911110003"; do
  IFS=: read -r ten name phone <<<"$t"
  psql_o <<SQL
INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES (gen_random_uuid(), '$ten', '$name', '$phone');
INSERT INTO conversations (id, tenant_id, contact_id) SELECT gen_random_uuid(), '$ten', id FROM contacts WHERE tenant_id = '$ten';
INSERT INTO messages (tenant_id, conversation_id, direction, body) SELECT '$ten', id, 'inbound', 'Mensagem de $name' FROM conversations WHERE tenant_id = '$ten';
INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status, capabilities) VALUES (gen_random_uuid(), '$ten', 'whatsapp', 'waha', 'unofficial', 'smoke-$phone', 'active', '["text"]');
UPDATE conversations SET channel_connection_id = (SELECT id FROM channel_connections WHERE tenant_id = '$ten') WHERE tenant_id = '$ten';
SQL
done

mkdir -p "$WORK/bin"
APPDB="postgres://omnira_app:omnira_app@127.0.0.1:$PGPORT/hubsmoke?sslmode=disable"
golang() { docker run --rm --name "$RUN-go-$RANDOM" --network host -e APIPORT="$APIPORT" -v "$PWD":/app -v "$WORK":/out -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
  -e OUT=/out -e APPDB="$APPDB" -e NATSPORT="$NATSPORT" -e TA="$TA" -e TB="$TB" -e TC="$TC" -e ITEM_C="${ITEM_C:-}" golang:1.25 sh -c "$1" 2>&1 | grep -v "^go: downloading"; }

echo "== phase A: build the real binaries, provision with omnira-hubctl, project with the reconciler"
golang '
set -e
go build -o /out/bin/api ./apps/api/cmd/omnira-api
go build -o /out/bin/hubctl ./apps/hubctl/cmd/omnira-hubctl
export OMNIRA_DATABASE_URL="$APPDB"
ctl() { /out/bin/hubctl --operator e2e "$@"; }
HUB=$(ctl hub create --name "K3G Service Desk" | awk "{print \$NF}")
ctl member add --hub "$HUB" --email test@omnira.local >/dev/null
for T in "$TA" "$TB" "$TC"; do ctl contract create --hub "$HUB" --tenant "$T" >/dev/null; done
ctl grant add --hub "$HUB" --tenant "$TA" --email test@omnira.local --reply >/dev/null
ctl grant add --hub "$HUB" --tenant "$TB" --email test@omnira.local >/dev/null
echo "$HUB" >/out/hub.id
ctl reconcile | tee /out/reconcile.txt
' | tee "$WORK/phaseA.txt"
HUB=$(cat "$WORK/hub.id")
ITEM_C=$(psql_o -c "SELECT id FROM hub_inbox_items WHERE tenant_id = '$TC' LIMIT 1")
[ -n "$ITEM_C" ] || { echo "FAIL: the reconciler did not project company C (it has a contract but no grant for the agent: the row must exist, ACCESS is what is denied)"; exit 1; }

echo "== phase B: the real omnira-api with dev login; flag ON, revoke, flag OFF"
golang '
set -e
export OMNIRA_DATABASE_URL="$APPDB" OMNIRA_ENV=test OMNIRA_AUTH_MODE=mock OMNIRA_DEV_AUTH_ENABLED=true OMNIRA_NATS_URL="nats://127.0.0.1:$NATSPORT" \
  OMNIRA_CREDENTIALS_KEY="$(head -c32 /dev/urandom | base64)" OMNIRA_HTTP_ADDR=127.0.0.1:$APIPORT
HUB=$(cat /out/hub.id)
start_api() { env "$@" /out/bin/api >"/out/api-$1.log" 2>&1 & echo $! >/out/api.pid; for i in $(seq 1 60); do wget -q -T 2 -O- http://127.0.0.1:$APIPORT/internal/health/live >/dev/null 2>&1 && return 0; sleep 0.5; done; echo "API did not start"; tail -20 "/out/api-$1.log"; exit 1; }
stop_api() { kill "$(cat /out/api.pid)" 2>/dev/null || true; sleep 1; }
get() { curl -s --max-time 15 -o "$3" -w "%{http_code}" -b "$1" "http://127.0.0.1:$APIPORT/api/v1$2"; }
login() { curl -s --max-time 15 -o /dev/null -w "%{http_code}" -c "$2" -H "Content-Type: application/json" -d "{\"email\":\"$1\"}" http://127.0.0.1:$APIPORT/api/v1/auth/dev/login; }
ctl() { OMNIRA_DATABASE_URL="$APPDB" /out/bin/hubctl --operator e2e "$@"; }

start_api OMNIRA_HUB_API_ENABLED=true
echo "$(login test@omnira.local /out/agent.jar) login agent"
echo "$(get /out/agent.jar /hubs /out/hubs.json) /hubs"
echo "$(get /out/agent.jar "/hubs/$HUB/inbox" /out/inbox.json) inbox"
echo "$(login admin@omnira.local /out/other.jar) login other"
echo "$(get /out/other.jar /hubs /out/other-hubs.json) other /hubs"
echo "$(get /out/other.jar "/hubs/$HUB/inbox" /out/other-inbox.json) other inbox"
echo "$(curl -s --max-time 15 -o /dev/null -w "%{http_code}" http://127.0.0.1:$APIPORT/api/v1/hubs) anonymous /hubs"
ITEM_A=$(sed -n "s/.*\"id\":\"\([0-9a-f-]*\)\",\"tenant_id\":\"$TA\".*/\1/p" /out/inbox.json | head -1)
echo "$(get /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A" /out/open_a.json) open item A"
echo "$(get /out/agent.jar "/hubs/$HUB/inbox/$ITEM_C" /out/open_c.json) open item C"
echo "$(get /out/agent.jar "/hubs/$HUB/inbox?tenant_id=$TC" /out/forged.json) forged tenant selector"
# ---- write path: A is reply-capable, B is read-only, C has no grant
ITEM_B=$(sed -n "s/.*\"id\":\"\([0-9a-f-]*\)\",\"tenant_id\":\"$TB\".*/\1/p" /out/inbox.json | head -1)
post() { curl -s --max-time 15 -o "$4" -w "%{http_code}" -b "$1" -H "Content-Type: application/json" ${5:+-H "Idempotency-Key: $5"} -d "$3" "http://127.0.0.1:$APIPORT/api/v1$2"; }
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/messages" "{\"expected_tenant_id\":\"$TA\",\"text\":\"antes de assumir\"}" /out/w1.json smoke-key-0001) reply before claim"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/claim" "{\"expected_tenant_id\":\"$TB\"}" /out/w2.json) claim with the wrong company on screen"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/claim" "{\"expected_tenant_id\":\"$TA\"}" /out/w3.json) claim A"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/messages" "{\"expected_tenant_id\":\"$TA\",\"text\":\"Olá, em que posso ajudar?\"}" /out/w4.json smoke-key-0002) reply A"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/messages" "{\"expected_tenant_id\":\"$TA\",\"text\":\"Olá, em que posso ajudar?\"}" /out/w5.json smoke-key-0002) reply A replay"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_B/claim" "{\"expected_tenant_id\":\"$TB\"}" /out/w6.json) claim B (read-only grant)"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_B/messages" "{\"expected_tenant_id\":\"$TB\",\"text\":\"x\"}" /out/w7.json smoke-key-0003) reply B (read-only grant)"
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_C/claim" "{\"expected_tenant_id\":\"$TC\"}" /out/w8.json) claim C (no grant)"
echo "$(post /out/other.jar "/hubs/$HUB/inbox/$ITEM_A/claim" "{\"expected_tenant_id\":\"$TA\"}" /out/w9.json) claim A by a user outside the hub"
ctl grant revoke --hub "$HUB" --tenant "$TB" --email test@omnira.local >/dev/null
echo "$(get /out/agent.jar "/hubs/$HUB/inbox" /out/inbox_after.json) inbox after revoking B"
ctl grant revoke --hub "$HUB" --tenant "$TA" --email test@omnira.local >/dev/null
echo "$(post /out/agent.jar "/hubs/$HUB/inbox/$ITEM_A/messages" "{\"expected_tenant_id\":\"$TA\",\"text\":\"depois de revogar\"}" /out/w10.json smoke-key-0004) reply A after revoking A"
stop_api

start_api OMNIRA_HUB_API_ENABLED=false
echo "$(login test@omnira.local /out/agent2.jar) login agent again"
echo "$(get /out/agent2.jar /hubs /out/off-hubs.json) /hubs when off"
echo "$(get /out/agent2.jar "/hubs/$HUB/inbox" /out/off-inbox.json) inbox when off"
stop_api
' | tee "$WORK/transcript.txt"

echo "== assertions"
python3 - "$WORK" "$TA" "$TB" "$TC" <<'PY'
import json,sys,re
w,ta,tb,tc=sys.argv[1:5]
t=open(w+'/transcript.txt').read()
def code(label):
    m=re.search(r'^(\d{3}) '+re.escape(label)+r'\s*$',t,re.M)
    assert m, f"no status line for {label!r}\n{t}"
    return int(m.group(1))
def load(n): return json.load(open(f'{w}/{n}'))
def ok(c,msg):
    print(("PASS: " if c else "FAIL: ")+msg)
    if not c: sys.exit(1)
ok(code('/hubs')==200 and len(load('hubs.json')['items'])==1 and load('hubs.json')['items'][0]['name']=='K3G Service Desk',"the agent sees exactly their hub")
inbox=load('inbox.json')
tenants={i['tenant_id'] for i in inbox['items']}
ok(code('inbox')==200 and tenants=={ta,tb},"the inbox holds companies A and B only (C has a contract but no grant for this agent)")
ok({i['tenant_name'] for i in inbox['items']}=={'ISP Roraima','NorteNet'},"each row carries the company's trade name")
raw=open(w+'/inbox.json').read()
ok(tc not in raw and 'Terceira' not in raw,"nothing of the ungranted company C appears in the response")
ok(code('other /hubs')==200 and load('other-hubs.json')['items']==[],"a user in no hub gets an empty hub list")
ok(code('other inbox')==404,"a user in no hub cannot open the hub inbox (404, same as a forged hub)")
ok(code('anonymous /hubs')==401,"anonymous is 401")
o=load('open_a.json')
ok(code('open item A')==200 and o['tenant']['name']=='ISP Roraima' and o['access']['source']=='hub' and any('Mensagem de' in m['body'] for m in o['messages']),"opening a granted item returns its conversation, read through the hub")
ok(code('open item C')==404 and tc not in open(w+'/open_c.json').read(),"the item of the ungranted company C cannot be opened, even with its real id (404, nothing leaked)")
ok(code('forged tenant selector')==400,"a client-supplied tenant_id is refused")
after=load('inbox_after.json')
ok(code('inbox after revoking B')==200 and {i['tenant_id'] for i in after['items']}=={ta},"revoking the grant on B removes B from the very next read")
ok(code('/hubs when off')==404 and code('inbox when off')==404,"with OMNIRA_HUB_API_ENABLED=false the Hub routes do not exist (404)")
ok(code('reply before claim')==409,"replying before claiming is refused (409)")
ok(code('claim with the wrong company on screen')==409,"a claim whose expected company differs from the item's is refused (409)")
ok(code('claim A')==200 and json.load(open(w+'/w3.json'))['changed'] is True,"claim on the reply-capable company succeeds")
ok(code('reply A')==202 and load('w4.json')['tenant']['name']=='ISP Roraima' and load('w4.json')['status']=='queued',"reply is queued and the response names the company it answered as")
ok(code('reply A replay')==200 and load('w5.json')['id']==load('w4.json')['id'],"the same Idempotency-Key replays the same message")
ok(code('claim B (read-only grant)')==403 and code('reply B (read-only grant)')==403,"a read-only grant can neither claim nor reply (403)")
ok(code('claim C (no grant)')==404,"no grant at all is the uniform 404")
ok(code('claim A by a user outside the hub')==404,"a user outside the hub cannot claim")
ok(code('reply A after revoking A')==404,"revoking the grant stops replying on the very next request")
rec=open(w+'/phaseA.txt').read()
ok('3 upserted' in rec,"hubctl reconcile projected the 3 conversations of the 3 contracted companies")
PY

echo "== database effects of the write path (read as the owner)"
SENT=$(psql_o -c "SELECT count(*) FROM messages WHERE tenant_id = '$TA' AND direction = 'outbound' AND sent_by_user_id = '$AGENT' AND body = 'Olá, em que posso ajudar?'")
JOBS=$(psql_o -c "SELECT count(*) FROM outbox_events WHERE tenant_id = '$TA' AND event_type = 'job.channel.send_text.v1'")
HELD=$(psql_o -c "SELECT count(*) FROM conversations WHERE tenant_id = '$TA' AND assigned_to_user_id = '$AGENT'")
AUD=$(psql_o -c "SELECT count(*) FROM audit_events WHERE tenant_id = '$TA' AND actor_id = '$AGENT' AND action IN ('hub.conversation.claimed','hub.message.sent')")
UNTOUCHED=$(psql_o -c "SELECT count(*) FROM messages WHERE direction = 'outbound' AND tenant_id IN ('$TB','$TC')")
[ "$SENT" = 1 ] && [ "$JOBS" = 1 ] && [ "$HELD" = 1 ] && [ "$AUD" = 2 ] && [ "$UNTOUCHED" = 0 ] \
  || { echo "FAIL: write effects wrong: sent=$SENT jobs=$JOBS held=$HELD audit=$AUD other-companies-outbound=$UNTOUCHED"; exit 1; }
echo "PASS: one message stored under company A as the agent, one delivery job, conversation held by the agent, 2 audit events, nothing written for B or C"
echo "PASS: end-to-end Hub smoke with the real binaries"
