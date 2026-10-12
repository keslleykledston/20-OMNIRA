#!/usr/bin/env bash
# E2E of the REAL sign-in: a real Keycloak (the same image and realm file production uses), the real omnira-api in OIDC mode, the built web app and a real browser
# walking through the Keycloak login form. Three people: one who belongs to a company (membership), one who only belongs to the Service Hub (no membership
# anywhere: the product's central case) and one who exists in Keycloak but was never given anything in OMNIRA; and the invitation by e-mail (ADR-0039 §3.10): a Hub
# administrator authorizes an address that has no account, and the first sign-in of that person applies it. Everything is throwaway (PostgreSQL, NATS, Keycloak, ports on 127.0.0.1); nothing here touches omnira_dev, the deployed
# stack or the production Keycloak.
#
# What this proves that scripts/e2e-hub-browser.sh does not: the login itself (code flow with PKCE, nonce, state, issuer check, verified e-mail, identity -> user,
# session cookie, the first screen of each kind of person). What it still does NOT prove: the production Keycloak/realm state, production DNS and TLS, the
# SMTP sender, a real WhatsApp channel.
#
# Usage: scripts/e2e-hub-keycloak.sh [playwright args]
set -euo pipefail
cd "$(dirname "$0")/.."
RUN=hubkc-$$
PG=omnira-$RUN-pg; NATS=omnira-$RUN-nats; KC=omnira-$RUN-kc
WORK=$(mktemp -d); chmod 700 "$WORK"
APIPORT=$((19000 + $$ % 900)); WEBPORT=$((20000 + $$ % 900)); KCPORT=$((21000 + $$ % 900))
LBL=(--label com.omnira.integration-test=true --label "com.omnira.integration-test.run=$RUN")
cleanup() {
  [ -f "$WORK/api.pid" ] && kill "$(cat "$WORK/api.pid")" 2>/dev/null || true
  [ -f "$WORK/web.pid" ] && kill "$(cat "$WORK/web.pid")" 2>/dev/null || true
  docker ps -aq --filter "name=$RUN" | xargs -r docker rm -fvv >/dev/null 2>&1 || true
  shred -u "$WORK"/realm.json "$WORK"/secrets.env 2>/dev/null || true
  rm -rf "$WORK" 2>/dev/null || true
}
trap cleanup EXIT
export OMNIRA_SESSION_CTX_KEY=c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5c5
rnd() { head -c 24 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
CLIENT_SECRET=$(rnd 32); KC_ADMIN_PW=$(rnd 20); PW_MEMBER="Aa1$(rnd 14)"; PW_HUBONLY="Aa1$(rnd 14)"; PW_GHOST="Aa1$(rnd 14)"; PW_ADMIN="Aa1$(rnd 14)"; PW_INVITED="Aa1$(rnd 14)"
ISSUER="http://127.0.0.1:$KCPORT/realms/omnira"
U_MEMBER=$(cat /proc/sys/kernel/random/uuid); U_HUBONLY=$(cat /proc/sys/kernel/random/uuid); U_GHOST=$(cat /proc/sys/kernel/random/uuid); U_ADMIN=$(cat /proc/sys/kernel/random/uuid); U_INVITED=$(cat /proc/sys/kernel/random/uuid)   # the Keycloak ids ARE the OIDC subjects

echo "== realm file for this run (production realm + this run's redirect URIs and two people)"
python3 - "$WORK/realm.json" "$APIPORT" "$WEBPORT" "$CLIENT_SECRET" "$U_MEMBER:$PW_MEMBER:member@e2e.test:Marta:Membro" "$U_HUBONLY:$PW_HUBONLY:hubonly@e2e.test:Hugo:SoHub" "$U_GHOST:$PW_GHOST:ghost@e2e.test:Gabi:Fantasma" "$U_ADMIN:$PW_ADMIN:admin@e2e.test:Aline:Admin" "$U_INVITED:$PW_INVITED:invited@e2e.test:Iara:Convidada" <<'PY'
import json, sys
out, api, web, secret, *people = sys.argv[1:]
d = json.load(open('deploy/keycloak/omnira-realm.json'))
c = next(c for c in d['clients'] if c['clientId'] == 'omnira-web')
c['secret'] = secret
c['redirectUris'] += [f'http://127.0.0.1:{web}/api/v1/auth/oidc/callback']
c['webOrigins'] += [f'http://127.0.0.1:{web}']
c.setdefault('attributes', {})['post.logout.redirect.uris'] = f'http://127.0.0.1:{web}/login'
d['users'] = []
for p in people:
    uid, pw, email, first, last = p.split(':')
    d['users'].append({'id': uid, 'username': email, 'email': email, 'emailVerified': True, 'enabled': True, 'firstName': first, 'lastName': last,
                       'credentials': [{'type': 'password', 'value': pw, 'temporary': False}]})
json.dump(d, open(out, 'w'))
PY
chmod 600 "$WORK/realm.json"

echo "== throwaway PostgreSQL, NATS and Keycloak"
docker run -d --name "$PG" "${LBL[@]}" -e POSTGRES_USER=omnira -e POSTGRES_PASSWORD=pw -e POSTGRES_DB=hubkc -p 127.0.0.1::5432 postgres:16-alpine >/dev/null
docker run -d --name "$NATS" "${LBL[@]}" -p 127.0.0.1::4222 nats:2.10-alpine -js >/dev/null
docker run -d --name "$KC" "${LBL[@]}" -p "127.0.0.1:$KCPORT:$KCPORT" \
  -e KEYCLOAK_ADMIN=admin -e KEYCLOAK_ADMIN_PASSWORD="$KC_ADMIN_PW" -e KC_HTTP_PORT="$KCPORT" -e KC_HOSTNAME="http://127.0.0.1:$KCPORT" -e KC_HOSTNAME_STRICT=true -e KC_HTTP_ENABLED=true \
  -v "$WORK/realm.json:/opt/keycloak/data/import/omnira-realm.json:ro" quay.io/keycloak/keycloak:25.0 start-dev --import-realm >/dev/null
for i in $(seq 1 90); do [ "$(docker logs "$PG" 2>&1 | grep -c 'database system is ready to accept connections')" -ge 2 ] && break; sleep 1; done
PGPORT=$(docker port "$PG" 5432/tcp | head -1 | cut -d: -f2); NATSPORT=$(docker port "$NATS" 4222/tcp | head -1 | cut -d: -f2)
psql_o() { docker exec -i "$PG" psql -U omnira -d hubkc -X -q -At -v ON_ERROR_STOP=1 "$@"; }

echo "== migrations (production migrator)"
docker run --rm --network "container:$PG" -v "$PWD/migrations:/migrations:ro" -v "$PWD/tools:/tools:ro" \
  -e PGHOST=127.0.0.1 -e PGUSER=omnira -e PGPASSWORD=pw -e PGDATABASE=hubkc -e OMNIRA_SESSION_CTX_KEY="$OMNIRA_SESSION_CTX_KEY" -e SESSION_CTX_MODE=enforce postgres:16-alpine sh /tools/migrate-sql.sh up | tail -1

echo "== build the real binaries and the web bundle"
mkdir -p "$WORK/bin"
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/api" ./apps/api/cmd/omnira-api
GOFLAGS=-buildvcs=false go build -o "$WORK/bin/hubctl" ./apps/hubctl/cmd/omnira-hubctl
(cd web && npm run build >/dev/null 2>&1) || { echo "web build failed"; exit 1; }
APPDB="postgres://omnira_app:omnira_app@127.0.0.1:$PGPORT/hubkc?sslmode=disable"
ctl() { OMNIRA_DATABASE_URL="$APPDB" "$WORK/bin/hubctl" --operator e2e "$@"; }

echo "== seed: two instances with one conversation each; a member of A; a person who only belongs to the Hub (grants on A and B)"
TA=a0000000-0000-0000-0000-00000000000a; TB=b0000000-0000-0000-0000-00000000000b
psql_o <<SQL
INSERT INTO users (id, external_subject, email, display_name) VALUES
  ('$U_MEMBER'::uuid, 'legacy-member', 'member@e2e.test', 'Marta Membro'),
  ('$U_HUBONLY'::uuid, 'legacy-hubonly', 'hubonly@e2e.test', 'Hugo SoHub'),
  ('$U_ADMIN'::uuid, 'legacy-admin', 'admin@e2e.test', 'Aline Admin');
INSERT INTO user_identities (user_id, issuer, subject, email, display_name, email_verified) VALUES
  ('$U_MEMBER'::uuid, '$ISSUER', '$U_MEMBER', 'member@e2e.test', 'Marta Membro', true),
  ('$U_HUBONLY'::uuid, '$ISSUER', '$U_HUBONLY', 'hubonly@e2e.test', 'Hugo SoHub', true),
  ('$U_ADMIN'::uuid, '$ISSUER', '$U_ADMIN', 'admin@e2e.test', 'Aline Admin', true);
INSERT INTO tenants (id, legal_name, trade_name, status) VALUES ('$TA','ISP Roraima Ltda','ISP Roraima','active'), ('$TB','NorteNet Telecom','NorteNet','active');
INSERT INTO memberships (tenant_id, user_id, role_id) SELECT '$TA', '$U_MEMBER'::uuid, id FROM roles WHERE tenant_id IS NULL AND key = 'tenant_agent' LIMIT 1;
SQL
for t in "$TA:Jose Carlos:+5592911110001" "$TB:Maria Souza:+5592911110002"; do
  IFS=: read -r ten name phone <<<"$t"
  psql_o <<SQL
INSERT INTO contacts (id, tenant_id, display_name, phone_e164) VALUES (gen_random_uuid(), '$ten', '$name', '$phone');
INSERT INTO conversations (id, tenant_id, contact_id) SELECT gen_random_uuid(), '$ten', id FROM contacts WHERE tenant_id = '$ten';
INSERT INTO messages (tenant_id, conversation_id, direction, body) SELECT '$ten', id, 'inbound', 'Mensagem de $name' FROM conversations WHERE tenant_id = '$ten';
SQL
done
HUB=$(ctl hub create --name "K3G Service Desk" | awk '{print $NF}')
ctl member add --hub "$HUB" --email hubonly@e2e.test >/dev/null
ctl member add --hub "$HUB" --email admin@e2e.test --role hub_admin >/dev/null
for T in "$TA" "$TB"; do ctl contract create --hub "$HUB" --tenant "$T" >/dev/null; done
ctl grant add --hub "$HUB" --tenant "$TA" --email hubonly@e2e.test --reply >/dev/null
ctl grant add --hub "$HUB" --tenant "$TB" --email hubonly@e2e.test --reply >/dev/null
ctl reconcile >/dev/null

echo "== wait for Keycloak"
for i in $(seq 1 120); do curl -sf --max-time 2 "$ISSUER/.well-known/openid-configuration" >/dev/null 2>&1 && break; sleep 1; done
curl -sf --max-time 2 "$ISSUER/.well-known/openid-configuration" >/dev/null || { echo "Keycloak did not come up"; docker logs "$KC" 2>&1 | tail -20; exit 1; }

echo "== the real omnira-api in OIDC mode (every Hub flag ON) and the built web app proxying to it"
OMNIRA_DATABASE_URL="$APPDB" OMNIRA_ENV=test OMNIRA_AUTH_MODE=oidc OMNIRA_AUTH_ISSUER="$ISSUER" OMNIRA_AUTH_CLIENT_ID=omnira-web OMNIRA_AUTH_AUDIENCE=omnira-web OMNIRA_AUTH_CLIENT_SECRET="$CLIENT_SECRET" \
  OMNIRA_AUTH_REDIRECT_URL="http://127.0.0.1:$WEBPORT/api/v1/auth/oidc/callback" OMNIRA_NATS_URL="nats://127.0.0.1:$NATSPORT" \
  OMNIRA_CREDENTIALS_KEY="$(head -c32 /dev/urandom | base64)" OMNIRA_HTTP_ADDR=127.0.0.1:$APIPORT \
  OMNIRA_HUB_API_ENABLED=true OMNIRA_HUB_ADMIN_API_ENABLED=true OMNIRA_HUB_ACCESS_API_ENABLED=true OMNIRA_HUB_SERVE_ENABLED=true \
  "$WORK/bin/api" >"$WORK/api.log" 2>&1 &
echo $! >"$WORK/api.pid"
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$APIPORT/internal/health/live" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 2 "http://127.0.0.1:$APIPORT/internal/health/live" >/dev/null || { echo "API did not start"; tail -30 "$WORK/api.log"; exit 1; }
(cd web && VITE_API_PROXY="http://127.0.0.1:$APIPORT" npx vite preview --port "$WEBPORT" --host 127.0.0.1 --strictPort >"$WORK/web.log" 2>&1 & echo $! >"$WORK/web.pid")
for i in $(seq 1 60); do curl -sf --max-time 2 "http://127.0.0.1:$WEBPORT/" >/dev/null 2>&1 && break; sleep 0.5; done
curl -sf --max-time 2 "http://127.0.0.1:$WEBPORT/" >/dev/null || { echo "web did not start"; tail -20 "$WORK/web.log"; exit 1; }

echo "== browser"
export E2E_BASE_URL="http://127.0.0.1:$WEBPORT" E2E_KC_URL="http://127.0.0.1:$KCPORT" E2E_HUB="$HUB" E2E_TA="$TA" E2E_ADMIN_EMAIL=admin@e2e.test E2E_ADMIN_PW="$PW_ADMIN" E2E_INVITED_EMAIL=invited@e2e.test E2E_INVITED_PW="$PW_INVITED"
export E2E_MEMBER_EMAIL=member@e2e.test E2E_MEMBER_PW="$PW_MEMBER" E2E_HUBONLY_EMAIL=hubonly@e2e.test E2E_HUBONLY_PW="$PW_HUBONLY" E2E_GHOST_EMAIL=ghost@e2e.test E2E_GHOST_PW="$PW_GHOST"
set +e
(cd web && npx playwright test -c playwright.oidc.config.ts "$@")
STATUS=$?
set -e
if [ "$STATUS" -ne 0 ]; then echo "--- api log (tail)"; tail -25 "$WORK/api.log"; exit "$STATUS"; fi
echo "PASS: real sign-in (Keycloak) E2E in a real browser"
