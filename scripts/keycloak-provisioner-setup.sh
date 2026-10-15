#!/usr/bin/env bash
# ADR-0046, once per Keycloak realm: lets OMNIRA prepare the sign-in account of invited people.
#   1. the realm's user profile keeps OMNIRA's account attributes (unmanaged attributes editable by ADMINS only: ADMIN_EDIT);
#      without it Keycloak drops them and the API refuses to create accounts (fail closed);
#   2. confidential client omnira-provisioner, service account only, holding ONLY realm-management view-users + manage-users;
#   3. its secret and OMNIRA_INVITE_PROVISION_LOGIN=true appended to .env (mode 600). The secret is never printed nor put on a
#      command line. Then recreate the api: docker compose up -d --no-deps --force-recreate api
# The master admin password is read from its 0600 file through stdin and handed to kcadm in KC_CLI_PASSWORD (never on a command line);
# kcadm keeps only short-lived tokens in a file removed at exit. Every step is idempotent, so a run that stopped half way can be run
# again: it keeps what is already there and finishes. It stops without changing anything if .env already holds a provisioner secret.
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

docker exec -i "$KC" sh -c 'read -r P; KC_CLI_PASSWORD="$P" /opt/keycloak/bin/kcadm.sh config credentials --server '"$KC_URL"' --realm master --user "$KEYCLOAK_ADMIN" --config '"$CFG"' >/dev/null' \
  < "$PW_FILE"
echo "admin login: ok"

# 1. user profile (saved first; it holds no secret)
mkdir -p "$BACKUP_DIR"
BEFORE="$BACKUP_DIR/users-profile-$REALM-$(date -u +%Y%m%dT%H%M%SZ).json"
kc get "realms/$REALM/users/profile" > "$BEFORE"
if [ "$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("unmanagedAttributePolicy"))' "$BEFORE")" != "ADMIN_EDIT" ]; then
  python3 -c 'import json,sys; d=json.load(open(sys.argv[1])); d["unmanagedAttributePolicy"]="ADMIN_EDIT"; print(json.dumps(d))' "$BEFORE" \
    | kc update "realms/$REALM/users/profile" -f - >/dev/null
fi
echo "user profile: unmanagedAttributePolicy=$(kc get "realms/$REALM/users/profile" | python3 -c 'import json,sys; print(json.load(sys.stdin).get("unmanagedAttributePolicy"))') (before: $BEFORE)"

# 2. the client (kept if a previous run created it) and its two roles
CID=$(kc get clients -r "$REALM" -q clientId=omnira-provisioner --fields id | python3 -c 'import json,sys; d=json.load(sys.stdin); print(d[0]["id"] if d else "")')
if [ -z "$CID" ]; then
  CID=$(kc create clients -r "$REALM" -s clientId=omnira-provisioner -s enabled=true -s publicClient=false -s serviceAccountsEnabled=true \
    -s standardFlowEnabled=false -s directAccessGrantsEnabled=false -s implicitFlowEnabled=false \
    -s 'description=OMNIRA API: prepares the sign-in account of invited people (ADR-0046). Service account only.' -i)
  echo "client omnira-provisioner created"
else
  echo "client omnira-provisioner already there: kept"
fi
# 2a. Before anything is added: the client must be exactly what this script creates - OpenID Connect, client-secret authentication,
#     confidential, service account only, enabled. An older client someone changed stops here.
kc get "clients/$CID" -r "$REALM" | python3 -c '
import json, sys
c = json.load(sys.stdin)
want = {"enabled": True, "publicClient": False, "bearerOnly": False, "serviceAccountsEnabled": True, "standardFlowEnabled": False,
        "directAccessGrantsEnabled": False, "implicitFlowEnabled": False}
bad = [k for k, v in want.items() if bool(c.get(k, False)) != v]
if c.get("authorizationServicesEnabled"):
    bad.append("authorizationServicesEnabled")
if c.get("protocol", "openid-connect") != "openid-connect":
    bad.append("protocol=" + str(c.get("protocol")))
if c.get("clientAuthenticatorType") != "client-secret":
    bad.append("clientAuthenticatorType=" + str(c.get("clientAuthenticatorType")))
if bad:
    print("STOP: client omnira-provisioner differs from the expected setup: " + ", ".join(bad))
    sys.exit(1)'
SAID=$(kc get "clients/$CID/service-account-user" -r "$REALM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["id"])')
RMID=$(kc get clients -r "$REALM" -q clientId=realm-management --fields id | python3 -c 'import json,sys; print(json.load(sys.stdin)[0]["id"])')

# 2b. What the service account REACHES - directly, through groups and through composite roles at any depth - may not go beyond the
#     realm's default role and realm-management view-users + manage-users (and what those contain). Roles are compared by id (never by
#     name: a role of another container may share a name). Any failed read stops the script. Checked before any role is added and after.
check_reach() { # $1 = before | after
  python3 - "$KC" "$CFG" "$REALM" "$SAID" "$RMID" "$1" <<'PYCHECK'
import json, subprocess, sys

container, cfg, realm, said, rmid, phase = sys.argv[1:7]


def kc(path):
    out = subprocess.run(["docker", "exec", container, "/opt/keycloak/bin/kcadm.sh", "get", path, "-r", realm, "--config", cfg],
                         check=True, capture_output=True, text=True).stdout
    return json.loads(out)


def stop(message):
    print("STOP: " + message)
    sys.exit(1)


def closure(start):  # ids of the roles reachable through composites, the start included
    seen, todo = set(), list(start)
    while todo:
        role = todo.pop()
        if role in seen:
            continue
        seen.add(role)
        if len(seen) > 2000:
            stop("composite roles too deep to check")
        todo += [r["id"] for r in kc(f"roles-by-id/{role}/composites")]
    return seen


groups = kc(f"users/{said}/groups")
if groups:
    stop("the service account of omnira-provisioner belongs to groups: " + ", ".join(g.get("path") or g.get("name", "?") for g in groups))
default = kc(f"realms/{realm}").get("defaultRole") or {}
if not default.get("id"):
    stop("the realm's default role is unknown")
wanted = {name: kc(f"clients/{rmid}/roles/{name}")["id"] for name in ("view-users", "manage-users")}
direct = kc(f"users/{said}/role-mappings")
extra = [r["name"] for r in direct.get("realmMappings", []) if r["id"] != default["id"]]
extra += [client + ":" + r["name"] for client, entry in (direct.get("clientMappings") or {}).items() for r in entry.get("mappings", [])
          if r["id"] not in wanted.values()]
if extra:
    stop("the service account holds more than view-users + manage-users: " + ", ".join(sorted(extra)))
# realm roles: only the default role and what it contains; realm-management (the admin powers): only view-users, manage-users and
# what THEY contain - whatever the source, the default role included (a default role widened with admin roles is refused too)
reach_realm = kc(f"users/{said}/role-mappings/realm/composite")
reach_admin = kc(f"users/{said}/role-mappings/clients/{rmid}/composite")
allowed_realm, allowed_admin = closure([default["id"]]), closure(list(wanted.values()))
extra = sorted({r["name"] for r in reach_realm if r["id"] not in allowed_realm})
extra += sorted({"realm-management:" + r["name"] for r in reach_admin if r["id"] not in allowed_admin})
if extra:
    stop("the service account holds more than view-users + manage-users: " + ", ".join(extra))
if phase == "after" and not set(wanted.values()) <= {r["id"] for r in reach_admin}:
    stop("view-users and manage-users are not both granted")
PYCHECK
}
check_reach before
# add-roles only finds roles not assigned yet: ask for the missing ones only
HAVE=$(kc get-roles -r "$REALM" --uusername service-account-omnira-provisioner --cclientid realm-management | python3 -c 'import json,sys; print(" ".join(r["name"] for r in json.load(sys.stdin)))')
for role in view-users manage-users; do
  case " $HAVE " in
    *" $role "*) ;;
    *) kc add-roles -r "$REALM" --uusername service-account-omnira-provisioner --cclientid realm-management --rolename "$role" ;;
  esac
done
check_reach after
echo "realm-management roles the service account reaches: $(kc get-roles -r "$REALM" --uusername service-account-omnira-provisioner --cclientid realm-management --effective | python3 -c 'import json,sys; print(sorted(r["name"] for r in json.load(sys.stdin)))')"

# 3. .env: 600 BEFORE the secret goes in
chmod 600 "$ENV_FILE"
SECRET=$(kc get "clients/$CID/client-secret" -r "$REALM" | python3 -c 'import json,sys; print(json.load(sys.stdin)["value"])')
[ -n "$SECRET" ] || { echo "STOP: empty client secret"; exit 1; }
{
  printf '\n# ADR-0046 (%s): OMNIRA prepares the Keycloak sign-in account of invited people (client omnira-provisioner,\n' "$(date -u +%Y-%m-%d)"
  printf '# realm-management view-users + manage-users only; api service only). Turn off with OMNIRA_INVITE_PROVISION_LOGIN=false.\n'
  printf 'OMNIRA_INVITE_PROVISION_LOGIN=true\n'
  printf 'OMNIRA_KEYCLOAK_PROVISIONER_SECRET=%s\n' "$SECRET"
} >> "$ENV_FILE"
unset SECRET
echo "$ENV_FILE: provisioner lines=$(grep -c -e '^OMNIRA_KEYCLOAK_PROVISIONER_SECRET=' -e '^OMNIRA_INVITE_PROVISION_LOGIN=' "$ENV_FILE") mode=$(stat -c %a "$ENV_FILE")"
echo "next: docker compose up -d --no-deps --force-recreate api"
