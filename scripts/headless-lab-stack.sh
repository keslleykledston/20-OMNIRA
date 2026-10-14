#!/usr/bin/env bash
# Isolated lab stack for the HEADLESS live test (ADR-0046). It is a SEPARATE docker compose project ("omnira-hl"): its own containers, network, volumes and database,
# on host ports 4xxxx; it never touches the production stack that may be running on the same host (no container_name, no shared port, no shared volume).
#
#   scripts/headless-lab-stack.sh gen      write the compose file and the .env of the lab (nothing is started)
#   scripts/headless-lab-stack.sh config   validate it with `docker compose config` (nothing is started)
#   scripts/headless-lab-stack.sh up       build and start it (api, worker, web, postgres, nats, valkey, clamav, waha, seed)
#   scripts/headless-lab-stack.sh status   list the containers and print the URLs
#   scripts/headless-lab-stack.sh logs [service]
#   scripts/headless-lab-stack.sh down     stop it AND delete its volumes (the lab database and the WhatsApp session are gone)
#
# The lab runs the code of the CURRENT checkout. The headless flags are ON in the lab and only in the lab.
set -euo pipefail
cd "$(dirname "$0")/.."
REPO=$PWD
LAB=${OMNIRA_HL_DIR:-/data/cafegpt/tmp/omnira-hl}
PROJECT=omnira-hl
mkdir -p "$LAB"

gen() {
  python3 - "$REPO" "$LAB" <<'PY'
import sys, re, secrets, yaml, os
repo, lab = sys.argv[1:3]
src = yaml.safe_load(open(os.path.join(repo, "docker-compose.yml")))
def remap(p):
    # "127.0.0.1:55434:5432" -> "127.0.0.1:45434:5432"; never a host port the production stack uses
    parts = str(p).split(":")
    if len(parts) == 3:
        host = int(parts[1]); parts[1] = str(40000 + host % 10000)
    elif len(parts) == 2:
        host = int(parts[0]); parts[0] = str(40000 + host % 10000)
    return ":".join(parts)
for name, svc in (src.get("services") or {}).items():
    svc.pop("container_name", None)
    if "ports" in svc:
        svc["ports"] = [remap(p) for p in svc["ports"]]
src["name"] = "omnira-hl"
with open(os.path.join(lab, "docker-compose.yml"), "w") as f:
    yaml.safe_dump(src, f, sort_keys=False)
env_path = os.path.join(lab, ".env")
if not os.path.exists(env_path):   # secrets are generated once, kept only in this file (mode 600), never printed
    key = secrets.token_hex(32)
    with open(env_path, "w") as f:
        f.write(f"""COMPOSE_PROJECT_NAME=omnira-hl
OMNIRA_ENV=development
OMNIRA_DB_NAME=omnira_hl
OMNIRA_APP_DB_PASSWORD={secrets.token_hex(12)}
POSTGRES_PASSWORD={secrets.token_hex(12)}
KEYCLOAK_DB_PASSWORD={secrets.token_hex(12)}
OMNIRA_CREDENTIALS_KEY={key}
WAHA_API_KEY={secrets.token_hex(16)}
OMNIRA_HEADLESS_ENABLED=true
OMNIRA_HEADLESS_WRITE_ENABLED=true
OMNIRA_HEADLESS_EVENT_RETENTION_DAYS=30
OMNIRA_HEADLESS_WEBHOOK_RETRY_HOURS=72
OMNIRA_PUBLIC_BASE_URL=http://127.0.0.1:43000
""")
    os.chmod(env_path, 0o600)
print("lab compose:", os.path.join(lab, "docker-compose.yml"))
print("lab env    :", env_path, "(secrets generated once; not printed)")
PY
}

dc() {
  docker compose -p "$PROJECT" --project-directory "$REPO" -f "$LAB/docker-compose.yml" --env-file "$LAB/.env" --profile dev --profile whatsapp-unofficial "$@"
}

case "${1:-}" in
  gen) gen ;;
  config) gen >/dev/null; dc config --quiet && echo "lab compose is valid" ;;
  up) gen >/dev/null; dc up -d --build
      echo; "$0" status ;;
  status) dc ps; echo
      echo "web      http://127.0.0.1:43000   (login in dev mode; create the company in the lab, not in production)"
      echo "api      http://127.0.0.1:48080   (external API under /api/v1/external)"
      echo "webhook  the receiver of the reference CRM listens where YOU choose (crm-sim listen), https only" ;;
  logs) shift; dc logs --tail=200 "$@" ;;
  down) dc down -v --remove-orphans; echo "lab removed (containers, network, volumes)" ;;
  *) sed -n 2,13p "$0"; exit 2 ;;
esac
