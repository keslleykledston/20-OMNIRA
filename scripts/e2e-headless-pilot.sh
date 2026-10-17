#!/usr/bin/env bash
# REHEARSAL of the headless pilot (ADR-0048, docs/ops/HEADLESS-PILOT-PLAN.md) with the REAL binaries - omnira-api, omnira-worker and the reference CRM (crm-sim) - against
# a throwaway Postgres (production migrator) and NATS. It walks the pilot steps in their real order and proves the points the pilot depends on:
#   1 flags off: the external surface does not exist
#   2 the administrator installs a read-only application limited to ONE queue; the token is shown once
#   3 the application sees only that queue's conversations (not another queue's, not a spam contact's, not another company's) and the journal reaches it
#   4 writes are refused until OMNIRA_HEADLESS_WRITE_ENABLED is on; then claim -> send -> release work, the message is queued, the audit names the application
#   5 another company's token cannot touch this company's conversation
#   6 suspending the application returns what it held and cuts its access at once
#   7 flags off again: the surface disappears; nothing was lost
# Not rehearsable on this host's loopback: the webhook PUSH (the SSRF guard refuses private addresses by design) - that part is proven by the Go integration tests and
# by the first real endpoint in the pilot. Nothing here touches omnira_dev or any real service.
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=hlpilot-$$
PG=omnira-$RUN-pg; NATS=omnira-$RUN-nats
WORK=$(mktemp -d); chmod 777 "$WORK"
cleanup() { docker ps -aq --filter "name=$RUN" | xargs -r docker rm -fvv >/dev/null 2>&1 || true; rm -rf "$WORK" 2>/dev/null || true; }
APIPORT=$(python3 -c 'import socket;s=socket.socket();s.bind(("127.0.0.1",0));print(s.getsockname()[1])')  # a free port: a fixed range collides with other services on a shared host
trap cleanup EXIT
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")

docker run -d --name "$PG" "${LBL[@]}" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=pilot -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
mkdir -p "$WORK/nats"; chmod 777 "$WORK/nats"
# JetStream sizes its streams against the free space of its store: keep the store on the volume with room (the /var of a busy host can be nearly full)
docker run -d --name "$NATS" "${LBL[@]}" -v "$WORK/nats:/data" -p 127.0.0.1::4222 nats:2.10-alpine -js -sd /data >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PGPORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2); NATSPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2)
psql_o() { docker exec -i "$PG" psql -U omnira -d pilot -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== migrations (production migrator)"
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=pilot postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1

echo "== seed: company PILOTO (admin = dev login; queues SUPORTE and FINANCEIRO; one line) and company OUTRA with its own admin and conversation"
ADMIN=22222222-2222-2222-2222-222222222222; ADMIN_B=aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa
TA=a0000000-0000-0000-0000-00000000000a; TB=b0000000-0000-0000-0000-00000000000b
Q1=a1000000-0000-0000-0000-000000000001; Q2=a1000000-0000-0000-0000-000000000002
LINE=a2000000-0000-0000-0000-000000000001
C_OK=a3000000-0000-0000-0000-000000000001; C_OTHERQ=a3000000-0000-0000-0000-000000000002; C_SPAM=a3000000-0000-0000-0000-000000000003; C_B=b3000000-0000-0000-0000-000000000001
psql_o <<SQL
INSERT INTO users (id, external_subject, email) VALUES ('$ADMIN','e2e-admin','test@omnira.local'), ('$ADMIN_B','e2e-admin-b','admin@omnira.local');
INSERT INTO tenants (id, legal_name, trade_name, status) VALUES ('$TA','Empresa Piloto','Piloto','active'), ('$TB','Outra Empresa','Outra','active');
INSERT INTO memberships (tenant_id, user_id, role_id, status) SELECT '$TA', '$ADMIN', id, 'active' FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL;
INSERT INTO memberships (tenant_id, user_id, role_id, status) SELECT '$TB', '$ADMIN_B', id, 'active' FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL;
INSERT INTO queues (id, tenant_id, name) VALUES ('$Q1','$TA','SUPORTE'), ('$Q2','$TA','FINANCEIRO');
INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status, capabilities) VALUES ('$LINE','$TA','whatsapp','waha','unofficial','pilot-line','active','["text"]');
INSERT INTO contacts (id, tenant_id, display_name, phone_e164, kind) VALUES
  ('a4000000-0000-0000-0000-000000000001','$TA','Cliente Suporte','+5592911110001','other'),
  ('a4000000-0000-0000-0000-000000000002','$TA','Cliente Financeiro','+5592911110002','other'),
  ('a4000000-0000-0000-0000-000000000003','$TA','Golpista','+5592911110003','spam'),
  ('b4000000-0000-0000-0000-000000000001','$TB','Cliente Outra','+5592911110004','other');
INSERT INTO conversations (id, tenant_id, contact_id, queue_id, channel_connection_id) VALUES
  ('$C_OK','$TA','a4000000-0000-0000-0000-000000000001','$Q1','$LINE'),
  ('$C_OTHERQ','$TA','a4000000-0000-0000-0000-000000000002','$Q2','$LINE'),
  ('$C_SPAM','$TA','a4000000-0000-0000-0000-000000000003','$Q1','$LINE');
INSERT INTO conversations (id, tenant_id, contact_id) VALUES ('$C_B','$TB','b4000000-0000-0000-0000-000000000001');
INSERT INTO messages (tenant_id, conversation_id, direction, body) VALUES
  ('$TA','$C_OK','inbound','Oi, preciso de ajuda com a fatura'), ('$TA','$C_OTHERQ','inbound','Segunda via'), ('$TA','$C_SPAM','inbound','Ganhe dinheiro'), ('$TB','$C_B','inbound','Oi da outra empresa');
SQL

mkdir -p "$WORK/bin"
APPDB="postgres://omnira_app:omnira_app@127.0.0.1:$PGPORT/pilot?sslmode=disable"
golang() { docker run --rm --name "$RUN-go-$RANDOM" --network host -e APIPORT="$APIPORT" -v "$PWD":/app -v "$WORK":/out -w /app -e GOCACHE=/tmp/gocache -e GOFLAGS=-buildvcs=false \
  -e APPDB="$APPDB" -e NATSPORT="$NATSPORT" -e TA="$TA" -e TB="$TB" -e Q1="$Q1" -e C_OK="$C_OK" -e C_OTHERQ="$C_OTHERQ" -e C_SPAM="$C_SPAM" -e C_B="$C_B" golang:1.25 sh -c "$1" 2>&1 | grep -v "^go: downloading"; }

echo "== build the real binaries and walk the pilot"
golang '
set -e
go build -o /out/bin/api ./apps/api/cmd/omnira-api
go build -o /out/bin/worker ./apps/worker/cmd/omnira-worker
go build -o /out/bin/crm-sim ./apps/crm-sim
export OMNIRA_DATABASE_URL="$APPDB" OMNIRA_ENV=test OMNIRA_AUTH_MODE=mock OMNIRA_DEV_AUTH_ENABLED=true OMNIRA_NATS_URL="nats://127.0.0.1:$NATSPORT" \
  OMNIRA_CREDENTIALS_KEY="$(head -c32 /dev/urandom | base64)" OMNIRA_HTTP_ADDR=127.0.0.1:$APIPORT
HOST="http://127.0.0.1:$APIPORT"; EXT="$HOST/api/v1/external"
stop_all() { for f in api worker; do [ -f /out/$f.pid ] && kill "$(cat /out/$f.pid)" 2>/dev/null || true; rm -f /out/$f.pid; done; sleep 1; }
start_all() { # $@ = env assignments for api and worker
  env "$@" /out/bin/api >/out/api.log 2>&1 & echo $! >/out/api.pid
  env "$@" OMNIRA_HTTP_ADDR=127.0.0.1:0 OMNIRA_WORKER_HTTP_ADDR=127.0.0.1:0 /out/bin/worker >/out/worker.log 2>&1 & echo $! >/out/worker.pid
  for i in $(seq 1 60); do kill -0 "$(cat /out/api.pid)" 2>/dev/null || { echo "API exited"; tail -20 /out/api.log; exit 1; }; wget -q -T 2 -O- $HOST/internal/health/live >/dev/null 2>&1 && return 0; sleep 0.5; done
  echo "API did not start"; tail -20 /out/api.log; exit 1; }
login() { curl -s --max-time 15 -o /dev/null -w "%{http_code}" -c "$2" -H "Content-Type: application/json" -d "{\"email\":\"$1\"}" $HOST/api/v1/auth/dev/login; }
adm() { curl -s --max-time 15 -o "$4" -w "%{http_code}" -b "$1" -X "$2" -H "Origin: $HOST" -H "Content-Type: application/json" -d "${5:-{\}}" "$HOST/api/v1/tenants/$3"; }
code() { curl -s --max-time 15 -o /dev/null -w "%{http_code}" -H "Authorization: Bearer $1" "$EXT$2"; }
sim() { _tok="$1"; shift; OMNIRA_EXTERNAL_URL="$EXT" OMNIRA_EXTERNAL_TOKEN="$_tok" timeout 25 /out/bin/crm-sim "$@"; }
token_of() { sed -n "s/.*\"token\":\"\([^\"]*\)\".*/\1/p" "$1" | head -1; }
id_of() { sed -n "s/.*\"installation\":{\"id\":\"\([0-9a-f-]*\)\".*/\1/p" "$1" | head -1; }

echo "--- step 1: flags OFF"
start_all OMNIRA_HEADLESS_ENABLED=false
echo "$(code x /me) external surface with the flag off"
stop_all

echo "--- step 2: read-only flags ON; the administrator installs the application, limited to the SUPORTE queue"
start_all OMNIRA_HEADLESS_ENABLED=true OMNIRA_HEADLESS_WRITE_ENABLED=false
echo "$(login test@omnira.local /out/adm.jar) login administrator"
echo "$(login admin@omnira.local /out/admb.jar) login administrator of the other company"
echo "$(adm /out/adm.jar POST $TA/integrations/apps /out/app.json "{\"name\":\"CRM piloto\",\"scopes\":[\"conversation.read\",\"message.read\",\"contact.read\",\"events.read\"],\"queue_ids\":[\"$Q1\"]}") install the read-only application"
TOKEN=$(token_of /out/app.json); APP=$(id_of /out/app.json)
echo "$([ -n "$TOKEN" ] && echo 1 || echo 0) token received once"
echo "$(adm /out/adm.jar GET $TA/integrations/apps/$APP /out/app2.json) read the application again"
echo "$(grep -c "$TOKEN" /out/app2.json) the token does not appear again"

echo "--- step 3: the application reads"
echo "$(code "$TOKEN" /me) me"
sim "$TOKEN" list open >/out/list1.txt 2>&1 || true
echo "$(grep -c "$C_OK" /out/list1.txt) sees the SUPORTE conversation"
echo "$(grep -c "$C_OTHERQ" /out/list1.txt) sees the FINANCEIRO conversation"
echo "$(grep -c "$C_SPAM" /out/list1.txt) sees the spam contact conversation"
echo "$(grep -c "$C_B" /out/list1.txt) sees the other company conversation"
echo "$(code "$TOKEN" /conversations/$C_OTHERQ) open a conversation outside its queue"
echo "$(code "$TOKEN" /conversations/$C_B) open another company conversation"
sleep 3
sim "$TOKEN" bootstrap >/out/boot.txt 2>&1 || true
echo "bootstrap: $(cat /out/boot.txt | tr "\n" " ")"
echo "$(code "$TOKEN" /conversations/$C_OK/claim) a write while the write flag is off"
stop_all

echo "--- step 4: WRITE flag ON; a new token with claim and send"
start_all OMNIRA_HEADLESS_ENABLED=true OMNIRA_HEADLESS_WRITE_ENABLED=true
echo "$(login test@omnira.local /out/adm.jar) login administrator again"
echo "$(adm /out/adm.jar PATCH $TA/integrations/apps/$APP /out/patch.json "{\"scopes\":[\"conversation.read\",\"message.read\",\"contact.read\",\"events.read\",\"conversation.claim\",\"message.send\"]}") widen the scopes"
sim "$TOKEN" claim $C_OK >/out/claim.txt 2>&1 || true
echo "claim: $(tr "\n" " " </out/claim.txt | cut -c1-200)"
sim "$TOKEN" list open >/out/list2.txt 2>&1 || true
V=$(sed -n "s/.*${C_OK}.* v\([0-9][0-9]*\) .*/\1/p" /out/list2.txt | head -1)
echo "$(grep -c "crm_application\|holder=crm" /out/list2.txt) the conversation is held by the application (version $V)"
sim "$TOKEN" send $C_OK "${V:-0}" "Olá, aqui é o CRM piloto" >/out/send.txt 2>&1 || true
echo "send: $(tr "\n" " " </out/send.txt | cut -c1-700)"
V2=$(sed -n "s/.*v\([0-9][0-9]*\).*/\1/p" /out/send.txt | head -1)
sleep 3
curl -s --max-time 15 -H "Authorization: Bearer $TOKEN" "$EXT/events?after=0&limit=200" >/out/events.json
echo "$(grep -o "conversation.control_changed.v1" /out/events.json | wc -l) journal: control changes reached the application"
echo "$(grep -o "message.created.v1" /out/events.json | wc -l) journal: message facts reached the application"
echo "$(grep -c "$C_OTHERQ\|$C_SPAM\|$C_B" /out/events.json) journal: facts of conversations outside its view"
sim "$TOKEN" claim $C_OTHERQ >/out/claim2.txt 2>&1 || true
echo "claim outside the queue: $(tr "\n" " " </out/claim2.txt | cut -c1-120)"

echo "--- step 5: another company token cannot touch this company"
echo "$(adm /out/admb.jar POST $TB/integrations/apps /out/appb.json "{\"name\":\"CRM B\",\"scopes\":[\"conversation.read\",\"conversation.claim\"]}") the other company installs its own application"
TOKEN_B=$(token_of /out/appb.json)
echo "$(code "$TOKEN_B" /conversations/$C_OK) other company reads our conversation"
sim "$TOKEN_B" claim $C_OK >/out/claimb.txt 2>&1 || true
echo "other company claims our conversation: $(tr "\n" " " </out/claimb.txt | cut -c1-120)"

echo "--- step 6: the supervisor suspends the application"
echo "$(adm /out/adm.jar POST $TA/integrations/apps/$APP/suspend /out/susp.json) suspend"
echo "$(code "$TOKEN" /me) the suspended application calls /me"
stop_all

echo "--- step 7: flags OFF again"
start_all OMNIRA_HEADLESS_ENABLED=false
echo "$(code "$TOKEN" /me) external surface after switching off"
stop_all
' | tee "$WORK/transcript.txt"

echo "== assertions"
python3 - "$WORK" <<'PY'
import re,sys
w=sys.argv[1]
t=open(w+'/transcript.txt').read()
def line(label):
    m=re.search(r'^(\S+) '+re.escape(label)+r'\s*$',t,re.M)
    assert m, f"no line for {label!r}\n{t}"
    return m.group(1)
def ok(c,msg):
    print(("PASS: " if c else "FAIL: ")+msg)
    if not c: sys.exit(1)
ok(line('external surface with the flag off')=='404',"with the flag off the external surface does not exist (404)")
ok(line('install the read-only application')=='201' and line('token received once')=='1',"the administrator installs the application and receives the token")
ok(line('the token does not appear again')=='0',"the token is shown once and never again")
ok(line('me')=='200',"the application authenticates")
ok(line('sees the SUPORTE conversation')=='1',"it sees the conversation of its queue")
ok(line('sees the FINANCEIRO conversation')=='0' and line('sees the spam contact conversation')=='0' and line('sees the other company conversation')=='0',"it does not see another queue, a spam contact or another company")
ok(line('open a conversation outside its queue')=='404' and line('open another company conversation')=='404',"opening one by id is the same uniform 404")
ok('cursor=' in t,"bootstrap (checkpoint, snapshot, events) completes")
ok(line('a write while the write flag is off') in ('404','405'),"a write is refused while the write flag is off")
ok(line('widen the scopes')=='200',"the administrator widens the scopes")
ok(re.search(r'^claim: .*(held|crm|control|version|"changed")',t,re.M) is not None,"the claim succeeds")
ok(re.search(r'^1 the conversation is held by the application \(version \d+\)',t,re.M) is not None,"the conversation is held by the application, with a control version")
ok(re.search(r'^send: .*(queued|202|accepted|message)',t,re.M) is not None,"the message is accepted (queued)")
ok(int(line('journal: control changes reached the application'))>=1 and int(line('journal: message facts reached the application'))>=1,"the journal delivers the control change and the message fact to the application")
ok(line('journal: facts of conversations outside its view')=='0',"the journal carries nothing of another queue, a spam contact or another company")
ok(re.search(r'^claim outside the queue: .*(404|not_found|forbidden|Not Found)',t,re.M) is not None,"a claim outside its queue is refused")
ok(line('other company reads our conversation')=='404',"another company's token reads nothing of ours (404)")
ok(re.search(r'^other company claims our conversation: .*(404|not_found)',t,re.M) is not None,"another company's token cannot claim ours")
ok(line('suspend')=='200',"the administrator suspends the application")
ok(line('the suspended application calls /me') in ('401','403'),"the suspended application is cut off at once")
ok(line('external surface after switching off')=='404',"switching the flag off removes the surface")
PY

echo "== database effects (read as the owner)"
HELD=$(psql_o -c "SELECT count(*) FROM conversations WHERE id = '$C_OK' AND control_kind IS NOT NULL")
OUT=$(psql_o -c "SELECT count(*) FROM messages WHERE tenant_id = '$TA' AND conversation_id = '$C_OK' AND direction = 'outbound' AND body LIKE '%CRM piloto%'")
AUD=$(psql_o -c "SELECT count(*) FROM audit_events WHERE tenant_id = '$TA' AND action = 'external_app.command'")
OTHER=$(psql_o -c "SELECT count(*) FROM conversations WHERE id IN ('$C_OTHERQ','$C_SPAM') AND control_kind IS NOT NULL")
OTHERB=$(psql_o -c "SELECT count(*) FROM conversations WHERE tenant_id = '$TB' AND control_kind IS NOT NULL")
SUSP=$(psql_o -c "SELECT count(*) FROM external_app_installations WHERE tenant_id = '$TA' AND status = 'suspended'")
echo "held-after-suspend=$HELD outbound=$OUT command-audits=$AUD other-held=$OTHER other-company-held=$OTHERB suspended=$SUSP"
[ "$HELD" = 0 ] && [ "$OUT" = 1 ] && [ "$AUD" -ge 2 ] && [ "$OTHER" = 0 ] && [ "$OTHERB" = 0 ] && [ "$SUSP" = 1 ] \
  || { echo "FAIL: effects wrong"; exit 1; }
echo "PASS: after the suspension nothing is held; the message sits in the conversation as an outbound of the application; every command is audited; nothing of another queue, a spam contact or another company was touched"
echo "PASS: headless pilot rehearsal with the real binaries"
