#!/usr/bin/env bash
# ADR-0046: KeycloakLogins against a REAL disposable Keycloak - the production image and realm file, set up by the SAME script production
# runs (scripts/keycloak-provisioner-setup.sh: custom attributes editable by admins only; the omnira-provisioner client whose service
# account may only view and manage users), with a public hostname that differs from the address the API calls, as in production.
# Proves: a new invitee gets an account whose password is the e-mailed one; only that invitation (a resend, or one that replaced it or
# came after it ended) refreshes it, an invitation of another instance cannot; a cancelled invitation turns it off; accounts created by
# hand are never touched; the setup script refuses a second run. Leaves nothing behind.
set -euo pipefail
cd "$(dirname "$0")/.."

KC="omnira-kclogins-$$"
PORT=$((20000 + RANDOM % 10000))
rnd() { head -c 48 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
ADMIN_PW=$(rnd 24)
PW_PENDING="Aa1$(rnd 12)"
PW_USED="Aa1$(rnd 12)"

WORK=$(mktemp -d)
cleanup() { docker rm -f "$KC" >/dev/null 2>&1 || true; rm -rf "$WORK"; }
trap cleanup EXIT

echo "== disposable Keycloak (quay.io/keycloak/keycloak:25.0, deploy/keycloak/omnira-realm.json) on 127.0.0.1:$PORT"
docker run -d --name "$KC" --label omnira.test=kclogins -p "127.0.0.1:$PORT:$PORT" \
  -e KEYCLOAK_ADMIN=admin -e KEYCLOAK_ADMIN_PASSWORD="$ADMIN_PW" -e KC_HTTP_PORT="$PORT" -e KC_HOSTNAME="https://auth.kclogins.test" \
  -e KC_HOSTNAME_STRICT=true \
  -e KC_HTTP_ENABLED=true -v "$PWD/deploy/keycloak/omnira-realm.json:/opt/keycloak/data/import/omnira-realm.json:ro" \
  quay.io/keycloak/keycloak:25.0 start-dev --import-realm >/dev/null
for _ in $(seq 1 120); do
  curl -sf "http://127.0.0.1:$PORT/realms/omnira/.well-known/openid-configuration" >/dev/null && break
  sleep 2
done

kc() { docker exec -i "$KC" /opt/keycloak/bin/kcadm.sh "$@" --config /tmp/kc.cfg; }
printf '%s\n' "$ADMIN_PW" | docker exec -i "$KC" sh -c 'read -r P; KC_CLI_PASSWORD="$P" /opt/keycloak/bin/kcadm.sh config credentials \
  --server "http://localhost:'"$PORT"'" --realm master --user admin --config /tmp/kc.cfg >/dev/null'

echo "== realm changes with the production script (scripts/keycloak-provisioner-setup.sh)"
install -m 600 /dev/null "$WORK/admin.pw" && printf '%s\n' "$ADMIN_PW" > "$WORK/admin.pw"
install -m 600 /dev/null "$WORK/env"
setup() {
  ENV_FILE="$WORK/env" KEYCLOAK_ADMIN_PW_FILE="$WORK/admin.pw" KEYCLOAK_CONTAINER="$KC" KEYCLOAK_INTERNAL_URL="http://localhost:$PORT" \
    KEYCLOAK_BACKUP_DIR="$WORK" scripts/keycloak-provisioner-setup.sh
}
setup
SECRET=$(grep '^OMNIRA_KEYCLOAK_PROVISIONER_SECRET=' "$WORK/env" | cut -d= -f2-)
[ -n "$SECRET" ] && grep -q '^OMNIRA_INVITE_PROVISION_LOGIN=true$' "$WORK/env" || { echo "FAIL: the setup script did not fill the env file"; exit 1; }
[ "$(stat -c %a "$WORK/env")" = 600 ] || { echo "FAIL: env file is not 600"; exit 1; }
if setup >/dev/null 2>&1; then echo "FAIL: a second setup run must stop"; exit 1; fi
echo "second setup run refused: ok"
# a run that stopped before writing .env is finished by running it again: same client, same secret
install -m 600 /dev/null "$WORK/env2"
RESUMED=$(ENV_FILE="$WORK/env2" KEYCLOAK_ADMIN_PW_FILE="$WORK/admin.pw" KEYCLOAK_CONTAINER="$KC" KEYCLOAK_INTERNAL_URL="http://localhost:$PORT" \
  KEYCLOAK_BACKUP_DIR="$WORK" scripts/keycloak-provisioner-setup.sh 2>&1) || { echo "FAIL: resumed run failed:"; echo "$RESUMED"; exit 1; }
grep -q "already there: kept" <<<"$RESUMED" || { echo "FAIL: resumed run did not keep the client:"; echo "$RESUMED"; exit 1; }
[ "$(grep '^OMNIRA_KEYCLOAK_PROVISIONER_SECRET=' "$WORK/env2" | cut -d= -f2-)" = "$SECRET" ] || { echo "FAIL: resumed run wrote another secret"; exit 1; }
echo "resumed setup run kept the client and its secret: ok"
# an existing client someone widened (one more realm-management role) stops the setup before its secret is used
kc add-roles -r omnira --uusername service-account-omnira-provisioner --cclientid realm-management --rolename view-realm
install -m 600 /dev/null "$WORK/env3"
if ENV_FILE="$WORK/env3" KEYCLOAK_ADMIN_PW_FILE="$WORK/admin.pw" KEYCLOAK_CONTAINER="$KC" KEYCLOAK_INTERNAL_URL="http://localhost:$PORT" \
  KEYCLOAK_BACKUP_DIR="$WORK" scripts/keycloak-provisioner-setup.sh >"$WORK/widened.log" 2>&1; then
  echo "FAIL: a widened client was accepted"; exit 1
fi
grep -q "STOP: the service account holds more than" "$WORK/widened.log" || { echo "FAIL: widened client not explained:"; cat "$WORK/widened.log"; exit 1; }
[ ! -s "$WORK/env3" ] || { echo "FAIL: the secret of a widened client was written"; exit 1; }
kc remove-roles -r omnira --uusername service-account-omnira-provisioner --cclientid realm-management --rolename view-realm
echo "widened client refused before its secret was used: ok"
refused() { # $1 = label, $2 = expected STOP text
  install -m 600 /dev/null "$WORK/env4"
  if ENV_FILE="$WORK/env4" KEYCLOAK_ADMIN_PW_FILE="$WORK/admin.pw" KEYCLOAK_CONTAINER="$KC" KEYCLOAK_INTERNAL_URL="http://localhost:$PORT" \
    KEYCLOAK_BACKUP_DIR="$WORK" scripts/keycloak-provisioner-setup.sh >"$WORK/refused.log" 2>&1; then
    echo "FAIL: $1 was accepted"; exit 1
  fi
  grep -q "$2" "$WORK/refused.log" || { echo "FAIL: $1 not explained:"; cat "$WORK/refused.log"; exit 1; }
  [ ! -s "$WORK/env4" ] || { echo "FAIL: the secret was written for $1"; exit 1; }
  echo "$1 refused before its secret was used: ok"
}
PCID=$(kc get clients -r omnira -q clientId=omnira-provisioner --fields id | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')
SAID=$(kc get "clients/$PCID/service-account-user" -r omnira | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
# admin power inherited through a group
GID=$(kc create groups -r omnira -s name=wide-admins -i)
kc add-roles -r omnira --gid "$GID" --cclientid realm-management --rolename manage-realm
kc update "users/$SAID/groups/$GID" -r omnira -s realm=omnira -s userId="$SAID" -s groupId="$GID" -n
refused "a service account in an admin group" "belongs to groups"
kc delete "users/$SAID/groups/$GID" -r omnira
kc delete "groups/$GID" -r omnira
# admin power reached through a composite realm role
kc create roles -r omnira -s name=wide-composite >/dev/null
kc add-roles -r omnira --rname wide-composite --cclientid realm-management --rolename manage-clients
kc add-roles -r omnira --uusername service-account-omnira-provisioner --rolename wide-composite
refused "a composite role with manage-clients" "holds more than view-users"
kc remove-roles -r omnira --uusername service-account-omnira-provisioner --rolename wide-composite
kc delete roles/wide-composite -r omnira
# admin power that comes with the realm's default role (every account would have it; the provisioner is refused all the same)
DR=$(kc get realms/omnira | python3 -c 'import json,sys; print(json.load(sys.stdin)["defaultRole"]["name"])')
kc add-roles -r omnira --rname "$DR" --cclientid realm-management --rolename manage-clients
refused "a default role widened with manage-clients" "realm-management:manage-clients"
kc remove-roles -r omnira --rname "$DR" --cclientid realm-management --rolename manage-clients
# a client that does not authenticate with its secret
kc update "clients/$PCID" -r omnira -s clientAuthenticatorType=client-jwt
refused "a client-jwt client" "clientAuthenticatorType=client-jwt"
kc update "clients/$PCID" -r omnira -s clientAuthenticatorType=client-secret

echo "== two accounts created by hand: one still waiting for its first password (like sistema2), one in use"
for u in manual-pending manual-used; do
  kc create users -r omnira -s "username=$u@kc.test" -s "email=$u@kc.test" -s enabled=true -s emailVerified=true \
    -s firstName=Manual -s lastName=Teste >/dev/null
done
kc set-password -r omnira --username manual-pending@kc.test --new-password "$PW_PENDING" --temporary
kc set-password -r omnira --username manual-used@kc.test --new-password "$PW_USED"

echo "== go test (real Keycloak)"
OMNIRA_TEST_KEYCLOAK_URL="http://127.0.0.1:$PORT" OMNIRA_TEST_KEYCLOAK_PROVISIONER_SECRET="$SECRET" \
OMNIRA_TEST_KEYCLOAK_MANUAL="manual-pending@kc.test:$PW_PENDING:Account is not fully set up,manual-used@kc.test:$PW_USED:" \
  go test ./internal/tenancy/adapters/ -run 'TestKeycloakLoginsAgainstARealKeycloak' -count=1 -v
