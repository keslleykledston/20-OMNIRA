#!/usr/bin/env bash
# ADR-0046, once per Keycloak realm: lets OMNIRA prepare the sign-in account of invited people.
#   1. the realm's user profile keeps OMNIRA's account attributes (unmanaged attributes editable by ADMINS only: ADMIN_EDIT);
#      without it Keycloak drops them and the API refuses to create accounts (fail closed);
#   2. confidential client omnira-provisioner, service account only, holding ONLY realm-management view-users + manage-users;
#   3. its secret and OMNIRA_INVITE_PROVISION_LOGIN=true appended to .env (mode 600). The secret is never printed nor put on a
#      command line. Then recreate the api: docker compose up -d --no-deps --force-recreate api
# The master admin password is read from its 0600 file through stdin; kcadm keeps only short-lived tokens in a file removed at exit.
# Stops without changing anything if the client already exists or .env already holds a provisioner secret.
# Undo: delete the client in the realm, set unmanagedAttributePolicy back (the previous profile is saved in backups/keycloak/), and remove
# the two lines from .env.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
ENV_FILE=${ENV_FILE:-$ROOT/.env}
PW_FILE=${KEYCLOAK_ADMIN_PW_FILE:-$HOME/.config/omnira/keycloak-master-admin.pw}
KC=${KEYCLOAK_CONTAINER:-omnira-keycloak}
KC_URL=${KEYCLOAK_INTERNAL_URL:-http://localhost:8888} # as seen from inside the Keycloak container
REALM=${KEYCLOAK_REALM:-omnira}
BACKUP_DIR=${KEYCLOAK_BACKUP_DIR:-$ROOT/backups/keycloak}
CFG=/tmp/kc-provisioner-setup.cfg
kc() { docker exec -i "$KC" /opt/keycloak/bin/kcadm.sh "$@" --config "$CFG"; }
cleanup() { docker exec "$KC" rm -f "$CFG" >/dev/null 2>&1 || true; }
trap cleanup EXIT

[ -r "$PW_FILE" ] || { echo "STOP: master admin password file $PW_FILE not readable"; exit 1; }
[ -f "$ENV_FILE" ] || { echo "STOP: $ENV_FILE not found"; exit 1; }
if grep -q '^OMNIRA_KEYCLOAK_PROVISIONER_SECRET=' "$ENV_FILE"; then echo "STOP: $ENV_FILE already has a provisioner secret"; exit 1; fi

docker exec -i "$KC" sh -c 'read -r P; /opt/keycloak/bin/kcadm.sh config credentials --server '"$KC_URL"' --realm master --user "$KEYCLOAK_ADMIN" --password "$P" --config '"$CFG"' >/dev/null' \
  < "$PW_FILE"
echo "admin login: ok"

if [ "$(kc get clients -r "$REALM" -q clientId=omnira-provisioner --fields id | python3 -c 'import json,sys; print(len(json.load(sys.stdin)))')" != "0" ]; then
  echo "STOP: client omnira-provisioner already exists in realm $REALM"; exit 1
fi

# 1. user profile (saved first; it holds no secret)
mkdir -p "$BACKUP_DIR"
BEFORE="$BACKUP_DIR/users-profile-$REALM-$(date -u +%Y%m%dT%H%M%SZ).json"
kc get "realms/$REALM/users/profile" > "$BEFORE"
python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["unmanagedAttributePolicy"]="ADMIN_EDIT"; print(json.dumps(d))' "$BEFORE" \
  | kc update "realms/$REALM/users/profile" -f - >/dev/null
echo "user profile: unmanagedAttributePolicy=$(kc get "realms/$REALM/users/profile" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("unmanagedAttributePolicy"))') (before: $BEFORE)"

# 2. the client and its two roles
CID=$(kc create clients -r "$REALM" -s clientId=omnira-provisioner -s enabled=true -s publicClient=false -s serviceAccountsEnabled=true \
  -s standardFlowEnabled=false -s directAccessGrantsEnabled=false -s implicitFlowEnabled=false \
  -s 'description=OMNIRA API: prepares the sign-in account of invited people (ADR-0046). Service account only.' -i)
kc add-roles -r "$REALM" --uusername service-account-omnira-provisioner --cclientid realm-management --rolename view-users --rolename manage-users
echo "client omnira-provisioner created; realm-management roles: $(kc get-roles -r "$REALM" --uusername service-account-omnira-provisioner --cclientid realm-management | python3 -c 'import json,sys; print(sorted(r["name"] for r in json.load(sys.stdin)))')"

# 3. .env
SECRET=$(kc get "clients/$CID/client-secret" -r "$REALM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"])')
[ -n "$SECRET" ] || { echo "STOP: empty client secret"; exit 1; }
{
  printf '\n# ADR-0046 (%s): OMNIRA prepares the Keycloak sign-in account of invited people (client omnira-provisioner,\n' "$(date -u +%Y-%m-%d)"
  printf '# realm-management view-users + manage-users only; api service only). Turn off with OMNIRA_INVITE_PROVISION_LOGIN=false.\n'
  printf 'OMNIRA_INVITE_PROVISION_LOGIN=true\n'
  printf 'OMNIRA_KEYCLOAK_PROVISIONER_SECRET=%s\n' "$SECRET"
} >> "$ENV_FILE"
unset SECRET
chmod 600 "$ENV_FILE"
echo "$ENV_FILE: provisioner lines=$(grep -c -e '^OMNIRA_KEYCLOAK_PROVISIONER_SECRET=' -e '^OMNIRA_INVITE_PROVISION_LOGIN=' "$ENV_FILE") mode=$(stat -c %a "$ENV_FILE")"
echo "next: docker compose up -d --no-deps --force-recreate api"
