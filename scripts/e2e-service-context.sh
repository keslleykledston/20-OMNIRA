#!/usr/bin/env bash
# End-to-end smoke of Service Context & Operations (ADR-0049) with the REAL omnira-api binary (dev login) against a throwaway Postgres (production migrator)
# and NATS. Nothing here touches omnira_dev or any real service; no WhatsApp: the channel is a row, the worker is not started (messages stay queued).
#
# Proves what the integration suites cannot: the wiring in main.go (the four flags), the middleware chain and the HTTP contract together:
#   assuming a conversation opens its protocol (KTG-1001), the customer is told (per-company switch), a task and an occurrence hang from it, the history lists them,
#   finalizing with a chosen result closes the protocol and tells the customer, a stranger sees nothing, and with the flags off the module does not exist.
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=svcsmoke-$$
PG=omnira-$RUN-pg; NATS=omnira-$RUN-nats
WORK=$(mktemp -d); chmod 777 "$WORK"
cleanup() { docker ps -aq --filter "name=$RUN" | xargs -r docker rm -fvv >/dev/null 2>&1 || true; rm -rf "$WORK" 2>/dev/null || true; }
APIPORT=$((19000 + $$ % 900))
trap cleanup EXIT
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")

docker run -d --name "$PG" "${LBL[@]}" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=svcsmoke -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
docker run -d --name "$NATS" "${LBL[@]}" -p 127.0.0.1::4222 nats:2.10-alpine -js >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PGPORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2); NATSPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2)
psql_o() { docker exec -i "$PG" psql -U omnira -d svcsmoke -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== migrations (production migrator)"
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=svcsmoke postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1

echo "== seed: company K3G Solutions with an agent (member, tenant_agent), a customer conversation with a text line; a stranger with no membership"
AGENT=22222222-2222-2222-2222-222222222222; STRANGER=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
TA=a0000000-0000-0000-0000-00000000000a; CONV=c0000000-0000-0000-0000-0000000000c1
psql_o <<SQL
INSERT INTO users (id, external_subject, email) VALUES ('$AGENT','e2e-agent','test@omnira.local'), ('$STRANGER','e2e-stranger','admin@omnira.local');
INSERT INTO tenants (id, legal_name, trade_name, status) VALUES ('$TA','K3G Solutions','K3G Solutions','active');
INSERT INTO memberships (tenant_id, user_id, role_id, status) SELECT '$TA', '$AGENT', id, 'active' FROM roles WHERE key = 'tenant_agent' AND tenant_id IS NULL;
INSERT INTO agent_profiles (tenant_id, membership_id) SELECT '$TA', id FROM memberships WHERE tenant_id = '$TA' AND user_id = '$AGENT';
INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES (gen_random_uuid(), '$TA', 'Maria Souza', '+5592911110002');
INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status, capabilities) VALUES (gen_random_uuid(), '$TA', 'whatsapp', 'waha', 'unofficial', 'smoke-line', 'active', '["text"]');
INSERT INTO conversations (id, tenant_id, contact_id, channel_connection_id) SELECT '$CONV', '$TA', c.id, (SELECT id FROM channel_connections WHERE tenant_id = '$TA') FROM contacts c WHERE c.tenant_id = '$TA';
INSERT INTO messages (tenant_id, conversation_id, direction, body) VALUES ('$TA', '$CONV', 'inbound', 'Oi, preciso de ajuda');
-- the company turns the protocol notice ON (it starts off) and reserves its prefix
INSERT INTO tenant_entitlements (tenant_id, capability, enabled) VALUES ('$TA', 'service_protocol_notice', true);
SQL

mkdir -p "$WORK/bin"
APPDB="postgres://omnira_app:omnira_app@127.0.0.1:$PGPORT/svcsmoke?sslmode=disable"
golang() { docker run --rm --name "$RUN-go-$RANDOM" --network host -e APIPORT="$APIPORT" -v "$PWD":/app -v "$WORK":/out -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
  -e OUT=/out -e APPDB="$APPDB" -e NATSPORT="$NATSPORT" -e TA="$TA" -e CONV="$CONV" golang:1.25 sh -c "$1" 2>&1 | grep -v "^go: downloading"; }

echo "== build the real binary and drive it over HTTP: flags ON, then OFF"
golang '
set -e
go build -o /out/bin/api ./apps/api/cmd/omnira-api
export OMNIRA_DATABASE_URL="$APPDB" OMNIRA_ENV=test OMNIRA_AUTH_MODE=mock OMNIRA_DEV_AUTH_ENABLED=true OMNIRA_NATS_URL="nats://127.0.0.1:$NATSPORT" \
  OMNIRA_CREDENTIALS_KEY="$(head -c32 /dev/urandom | base64)" OMNIRA_HTTP_ADDR=127.0.0.1:$APIPORT
start_api() { env "$@" /out/bin/api >"/out/api.log" 2>&1 & echo $! >/out/api.pid; for i in $(seq 1 60); do wget -q -T 2 -O- http://127.0.0.1:$APIPORT/internal/health/live >/dev/null 2>&1 && return 0; sleep 0.5; done; echo "API did not start"; tail -20 /out/api.log; exit 1; }
stop_api() { kill "$(cat /out/api.pid)" 2>/dev/null || true; sleep 1; }
B="http://127.0.0.1:$APIPORT/api/v1/tenants/$TA"
login() { curl -s --max-time 15 -o /dev/null -w "%{http_code}" -c "$2" -H "Content-Type: application/json" -d "{\"email\":\"$1\"}" http://127.0.0.1:$APIPORT/api/v1/auth/dev/login; }
get() { curl -s --max-time 15 -o "$3" -w "%{http_code}" -b "$1" "$B$2"; }
post() { curl -s --max-time 15 -o "$4" -w "%{http_code}" -b "$1" -H "Origin: http://127.0.0.1:$APIPORT" -H "Content-Type: application/json" ${5:+-H "Idempotency-Key: $5"} ${6:+-H "If-Match: $6"} -d "$3" "$B$2"; }

start_api OMNIRA_SERVICE_CONTEXT_ENABLED=true OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED=true OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED=true OMNIRA_SERVICE_PROTOCOL_NOTICE_ENABLED=true
echo "$(login test@omnira.local /out/agent.jar) login agent"
echo "$(login admin@omnira.local /out/stranger.jar) login stranger"
echo "$(get /out/agent.jar "/conversations/$CONV/service-session" /out/s0.json) protocol before assuming"
echo "$(post /out/agent.jar "/inbox/conversations/$CONV/assign" "{}" /out/claim.json) assume the conversation"
echo "$(get /out/agent.jar "/conversations/$CONV/service-session" /out/s1.json) protocol after assuming"
SID=$(sed -n "s/.*\"id\":\"\([0-9a-f-]*\)\",\"protocol\".*/\1/p" /out/s1.json | head -1)
VER=$(sed -n "s/.*\"version\":\([0-9]*\).*/\1/p" /out/s1.json | head -1)
echo "$(post /out/agent.jar "/service-tasks" "{\"title\":\"Ligar para o cliente\",\"session_id\":\"$SID\"}" /out/task.json e2e-task-0001) create a task"
echo "$(post /out/agent.jar "/service-cases" "{\"title\":\"Cabo rompido\",\"session_id\":\"$SID\"}" /out/case.json e2e-case-0001) open an occurrence"
echo "$(get /out/agent.jar "/service-cases?session_id=$SID" /out/cases.json) occurrences of the protocol"
echo "$(get /out/agent.jar "/service-sessions/$SID/timeline" /out/timeline.json) history of the protocol"
echo "$(get /out/stranger.jar "/conversations/$CONV/service-session" /out/stranger1.json) stranger reads the protocol"
echo "$(post /out/stranger.jar "/service-tasks" "{\"title\":\"x\",\"session_id\":\"$SID\"}" /out/stranger2.json e2e-task-0002) stranger creates a task"
echo "$(get /out/agent.jar "/service-settings/protocol-prefix" /out/prefix.json) protocol prefix of the company"
echo "$(post /out/agent.jar "/inbox/conversations/$CONV/finalize" "{\"reason\":\"resolved\",\"summary\":\"Link voltou\",\"service_result\":\"resolved_first_contact\"}" /out/final.json) finalize choosing the result"
echo "$(get /out/agent.jar "/conversations/$CONV/service-session" /out/s2.json) protocol after finalizing"
stop_api

start_api OMNIRA_SERVICE_CONTEXT_ENABLED=false
echo "$(login test@omnira.local /out/agent2.jar) login agent again"
echo "$(get /out/agent2.jar "/conversations/$CONV/service-session" /out/off.json) protocol with the module off"
echo "$(get /out/agent2.jar "/service-cases" /out/off2.json) occurrences with the module off"
stop_api
' | tee "$WORK/transcript.txt"

echo "== assertions"
python3 - "$WORK" <<'PY'
import json,sys,re
w=sys.argv[1]
t=open(w+'/transcript.txt').read()
def code(label):
    m=re.search(r'^(\d{3}) '+re.escape(label)+r'\s*$',t,re.M)
    assert m, f"no status line for {label!r}\n{t}"
    return int(m.group(1))
def load(n): return json.load(open(f'{w}/{n}'))
def ok(c,msg):
    print(("PASS: " if c else "FAIL: ")+msg)
    if not c: sys.exit(1)
ok(code('login agent')==200 and code('login stranger')==200,"both dev users log in")
ok(code('protocol before assuming')==404,"before anyone assumes the conversation there is no protocol")
ok(code('assume the conversation')==200,"the agent assumes the conversation")
s1=load('s1.json')
ok(code('protocol after assuming')==200 and re.fullmatch(r'[A-Z]{2,6}-1001',s1['protocol']) is not None and s1['status']=='open',"assuming opened the protocol, numbered from 1001 with the company prefix ("+s1['protocol']+")")
ok(code('create a task')==201 and code('open an occurrence')==201,"a task and an occurrence hang from the protocol")
cases=load('cases.json')['items']
ok(code('occurrences of the protocol')==200 and len(cases)==1 and cases[0]['title']=='Cabo rompido' and re.fullmatch(r'OC-\d+',cases[0]['case_number']),"the occurrences list shows it with its OC number")
types={e['event_type'] for e in load('timeline.json')['items']}
ok(code('history of the protocol')==200 and {'service_session.opened','service_task.created','service_case.opened'} <= types,"the history lists the opening, the task and the occurrence")
ok(code('stranger reads the protocol') in (403,404) and s1['id'] not in open(w+'/stranger1.json').read(),"a user with no membership cannot read the protocol (nothing leaked)")
ok(code('stranger creates a task') in (403,404),"a user with no membership cannot create a task")
ok(code('protocol prefix of the company')==200 and load('prefix.json')['locked'] is True,"the prefix is locked once the first protocol exists")
ok(code('finalize choosing the result')==200,"finalizing with a chosen result succeeds")
s2=load('s2.json')
ok(code('protocol after finalizing')==200 and s2['status']=='closed' and s2.get('resolution_canonical')=='resolved_first_contact',"the protocol closed with the result the agent chose")
ok(code('protocol with the module off')==404 and code('occurrences with the module off')==404,"with the flags off the module does not exist (404)")
PY

echo "== database effects (read as the owner)"
SESS=$(psql_o -c "SELECT count(*) FROM service_sessions WHERE tenant_id = '$TA' AND conversation_id = '$CONV'")
NOTICES=$(psql_o -c "SELECT count(*) FROM messages WHERE tenant_id = '$TA' AND conversation_id = '$CONV' AND direction = 'outbound' AND body LIKE '%Protocolo de atendimento: %'")
OPENED=$(psql_o -c "SELECT count(*) FROM messages WHERE tenant_id = '$TA' AND conversation_id = '$CONV' AND direction = 'outbound' AND body LIKE '%Seu atendimento foi registrado%'")
CLOSED=$(psql_o -c "SELECT count(*) FROM messages WHERE tenant_id = '$TA' AND conversation_id = '$CONV' AND direction = 'outbound' AND body LIKE '%Seu atendimento foi encerrado%'")
CONVCLOSED=$(psql_o -c "SELECT count(*) FROM conversations WHERE id = '$CONV' AND status = 'closed'")
EVENTS=$(psql_o -c "SELECT count(*) FROM context_events WHERE tenant_id = '$TA' AND event_type = 'service_session.closed'")
[ "$SESS" = 1 ] && [ "$NOTICES" = 2 ] && [ "$OPENED" = 1 ] && [ "$CLOSED" = 1 ] && [ "$CONVCLOSED" = 1 ] && [ "$EVENTS" = 1 ] \
  || { echo "FAIL: effects wrong: sessions=$SESS notices=$NOTICES opened=$OPENED closed=$CLOSED conversation-closed=$CONVCLOSED close-events=$EVENTS"; exit 1; }
echo "PASS: one protocol for the conversation, the customer was told once when it opened and once when it ended (2 messages), the conversation is closed, one close event"
echo "PASS: end-to-end Service Context smoke with the real binary"
