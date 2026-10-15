#!/usr/bin/env bash
# ADR-0046: KeycloakLogins against a REAL disposable Keycloak - the production image and realm file, plus the two realm changes production
# receives (custom attributes editable by admins only; the omnira-provisioner client whose service account may only view and manage users).
# Proves: a new invitee gets an account whose password is the e-mailed one, a resend or a new invitation refreshes only an unused account
# OMNIRA created, a cancelled invitation turns it off, and accounts created by hand are never touched. Leaves nothing behind.
set -euo pipefail
cd "$(dirname "$0")/.."

KC="omnira-kclogins-$$"
PORT=$((20000 + RANDOM % 10000))
rnd() { head -c 48 /dev/urandom | base64 | tr -dc 'A-Za-z0-9' | head -c "$1"; }
ADMIN_PW=$(rnd 24)
PW_PENDING="Aa1$(rnd 12)"
PW_USED="Aa1$(rnd 12)"

cleanup() { docker rm -f "$KC" >/dev/null 2>&1 || true; }
trap cleanup EXIT

echo "== disposable Keycloak (quay.io/keycloak/keycloak:25.0, deploy/keycloak/omnira-realm.json) on 127.0.0.1:$PORT"
docker run -d --name "$KC" --label omnira.test=kclogins -p "127.0.0.1:$PORT:$PORT" \
  -e KEYCLOAK_ADMIN=admin -e KEYCLOAK_ADMIN_PASSWORD="$ADMIN_PW" -e KC_HTTP_PORT="$PORT" -e KC_HOSTNAME="http://127.0.0.1:$PORT" \
  -e KC_HTTP_ENABLED=true -v "$PWD/deploy/keycloak/omnira-realm.json:/opt/keycloak/data/import/omnira-realm.json:ro" \
  quay.io/keycloak/keycloak:25.0 start-dev --import-realm >/dev/null
for _ in $(seq 1 120); do
  curl -sf "http://127.0.0.1:$PORT/realms/omnira/.well-known/openid-configuration" >/dev/null && break
  sleep 2
done

kc() { docker exec -i "$KC" /opt/keycloak/bin/kcadm.sh "$@" --config /tmp/kc.cfg; }
docker exec "$KC" /opt/keycloak/bin/kcadm.sh config credentials --server "http://localhost:$PORT" --realm master --user admin \
  --password "$ADMIN_PW" --config /tmp/kc.cfg >/dev/null

echo "== realm changes (same as production): admin-only custom attributes, provisioner client"
kc get realms/omnira/users/profile | python3 -c 'import json,sys; d=json.load(sys.stdin); d["unmanagedAttributePolicy"]="ADMIN_EDIT"; print(json.dumps(d))' \
  | kc update realms/omnira/users/profile -f - >/dev/null
CID=$(kc create clients -r omnira -s clientId=omnira-provisioner -s enabled=true -s publicClient=false -s serviceAccountsEnabled=true \
  -s standardFlowEnabled=false -s directAccessGrantsEnabled=false -s implicitFlowEnabled=false -i)
kc add-roles -r omnira --uusername service-account-omnira-provisioner --cclientid realm-management --rolename view-users --rolename manage-users
SECRET=$(kc get "clients/$CID/client-secret" -r omnira | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"])')

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
