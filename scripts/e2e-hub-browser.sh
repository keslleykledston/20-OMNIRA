#!/usr/bin/env bash
# E2E of the Service Hub in a REAL BROWSER against the REAL binaries (ADR-0038 phase 5): omnira-api (dev login, every Hub flag ON),
# omnira-hubctl (provisioning, reconcile, distribute), the built web app served by `vite preview` and proxying /api to that API, and a
# throwaway PostgreSQL (production migrator, non-superuser application role, RLS on) + NATS. Nothing here touches omnira_dev, the
# deployed stack, a real channel or Keycloak.
#
# What this proves that the mock browser specs and the Go suites do not: the whole path screen -> proxy -> real middleware -> real RLS
# for what a person can and cannot see/do, delegation of channel management, work pools and transfer, with the real application bundle.
# What it still does NOT prove: real Keycloak sign-in, a real WhatsApp channel (the CRM/ERP connection is only STORED here), real webhooks.
#
# Usage: scripts/e2e-hub-browser.sh [playwright args]   (e.g. -g "transferir")
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=hubbrowser-$$
PG=omnira-$RUN-pg; NATS=omnira-$RUN-nats
WORK=$(mktemp -d); chmod 777 "$WORK"
APIPORT=$((19000 + $$ % 900)); WEBPORT=$((20000 + $$ % 900))
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")
cleanup() {
  [ -f "$WORK/api.pid" ] && kill "$(cat "$WORK/api.pid")" 2>/dev/null || true
  [ -f "$WORK/web.pid" ] && kill "$(cat "$WORK/web.pid")" 2>/dev/null || true
  docker ps -aq --filter "name=$RUN" | xargs -r docker rm -fvv >/dev/null 2>&1 || true
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT

docker run -d --name "$PG" "${LBL[@]}" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=hubbrowser -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
docker run -d --name "$NATS" "${LBL[@]}" -p 127.0.0.1::4222 nats:2.10-alpine -js >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PGPORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2); NATSPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2)
psql_o() { docker exec -i "$PG" psql -U omnira -d hubbrowser -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== migrations (production migrator)"
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=hubbrowser postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1

echo "== build the real binaries (host toolchain) and the web bundle"
mkdir -p "$WORK/bin"
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/api" ./apps/api/cmd/omnira-api
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/hubctl" ./apps/hubctl/cmd/omnira-hubctl
(cd web && npm run build >/dev/null 2>&1) || { echo "web build failed"; exit 1; }
APPDB="postgres://omnira_app:omnira_app@127.0.0.1:$PGPORT/hubbrowser?sslmode=disable"
ctl() { OMNIRA_DATABASE_URL="$APPDB" "$WORK/bin/hubctl" --operator e2e "$@"; }

echo "== seed: 3 instances with one WhatsApp line and one conversation each; five people"
TA=a0000000-0000-0000-0000-00000000000a; TB=b0000000-0000-0000-0000-00000000000b; TC=c0000000-0000-0000-0000-00000000000c
psql_o <<SQL
INSERT INTO users (id, external_subject, email, display_name) VALUES
  (gen_random_uuid(), 'e2e-admin',    'admin@e2e.test',    'Aline Admin'),
  (gen_random_uuid(), 'e2e-agent1',   'agent1@e2e.test',   'Bruno Agente'),
  (gen_random_uuid(), 'e2e-agent2',   'agent2@e2e.test',   'Carla Agente'),
  (gen_random_uuid(), 'e2e-reader',   'reader@e2e.test',   'Davi Leitor'),
  (gen_random_uuid(), 'e2e-stranger', 'stranger@e2e.test', 'Elisa Fora');
INSERT INTO tenants (id, legal_name, trade_name, status) VALUES ('$TA','ISP Roraima Ltda','ISP Roraima','active'), ('$TB','NorteNet Telecom','NorteNet','active'), ('$TC','Terceira Empresa','','active');
SQL
for t in "$TA:Jose Carlos:+5592911110001" "$TB:Maria Souza:+5592911110002" "$TC:Ana Lima:+5592911110003"; do
  IFS=: read -r ten name phone <<<"$t"
  psql_o <<SQL
INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES (gen_random_uuid(), '$ten', '$name', '$phone');
INSERT INTO conversations (id, tenant_id, contact_id) SELECT gen_random_uuid(), '$ten', id FROM contacts WHERE tenant_id = '$ten';
INSERT INTO messages (tenant_id, conversation_id, direction, body) SELECT '$ten', id, 'inbound', 'Mensagem de $name' FROM conversations WHERE tenant_id = '$ten';
INSERT INTO channel_connections (id, tenant_id, channel, provider, provider_kind, external_number_id, status, capabilities) VALUES (gen_random_uuid(), '$ten', 'whatsapp', 'waha', 'unofficial', 'e2e-$phone', 'active', '["text"]');
UPDATE conversations SET channel_connection_id = (SELECT id FROM channel_connections WHERE tenant_id = '$ten') WHERE tenant_id = '$ten';
SQL
done
HUB=$(ctl hub create --name "K3G Service Desk" | awk '{print $NF}')
ctl member add --hub "$HUB" --email admin@e2e.test  --role hub_admin >/dev/null
ctl member add --hub "$HUB" --email agent1@e2e.test >/dev/null
ctl member add --hub "$HUB" --email agent2@e2e.test >/dev/null
ctl member add --hub "$HUB" --email reader@e2e.test >/dev/null
for T in "$TA" "$TB" "$TC"; do ctl contract create --hub "$HUB" --tenant "$T" >/dev/null; done
ctl platform-operator add --email admin@e2e.test >/dev/null
# agent1: answers A and B; agent2: answers A; reader: only reads A
ctl grant add --hub "$HUB" --tenant "$TA" --email agent1@e2e.test --reply >/dev/null
ctl grant add --hub "$HUB" --tenant "$TB" --email agent1@e2e.test --reply >/dev/null
ctl grant add --hub "$HUB" --tenant "$TA" --email agent2@e2e.test --reply >/dev/null
ctl grant add --hub "$HUB" --tenant "$TA" --email reader@e2e.test >/dev/null
ctl reconcile >/dev/null

echo "== the real omnira-api (dev login, every Hub flag ON) and the built web app proxying to it"
OMNIRA_DATABASE_URL="$APPDB" OMNIRA_ENV=test OMNIRA_AUTH_MODE=mock OMNIRA_DEV_AUTH_ENABLED=true OMNIRA_NATS_URL="nats://127.0.0.1:$NATSPORT" \
  OMNIRA_CREDENTIALS_KEY="$(head -c32 /dev/urandom | base64)" OMNIRA_HTTP_ADDR=127.0.0.1:$APIPORT \
  OMNIRA_HUB_API_ENABLED=true OMNIRA_HUB_ADMIN_API_ENABLED=true OMNIRA_HUB_ACCESS_API_ENABLED=true \
  "$WORK/bin/api" >"$WORK/api.log" 2>&1 &
echo $! >"$WORK/api.pid"
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$APIPORT/internal/health/live" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 2 "http://127.0.0.1:$APIPORT/internal/health/live" >/dev/null || { echo "API did not start"; tail -30 "$WORK/api.log"; exit 1; }
(cd web && VITE_API_PROXY="http://127.0.0.1:$APIPORT" npx vite preview --port "$WEBPORT" --host 127.0.0.1 --strictPort >"$WORK/web.log" 2>&1 & echo $! >"$WORK/web.pid")
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$WEBPORT/" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 2 "http://127.0.0.1:$WEBPORT/" >/dev/null || { echo "web did not start"; tail -20 "$WORK/web.log"; exit 1; }

echo "== browser"
export E2E_BASE_URL="http://127.0.0.1:$WEBPORT" E2E_HUB="$HUB" E2E_TA="$TA" E2E_TB="$TB" E2E_TC="$TC"
export E2E_PSQL="docker exec -i $PG psql -U omnira -d hubbrowser -X -q -At -v ON_ERROR_STOP=1"
export E2E_HUBCTL="env OMNIRA_DATABASE_URL=$APPDB $WORK/bin/hubctl --operator e2e"
set +e
(cd web && npx playwright test -c playwright.real.config.ts "$@")
STATUS=$?
set -e
if [ "$STATUS" -ne 0 ]; then echo "--- api log (tail)"; tail -25 "$WORK/api.log"; exit "$STATUS"; fi
echo "PASS: Hub E2E in a real browser against the real binaries"
