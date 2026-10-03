#!/usr/bin/env bash
# Creates (or finds) the healthchecks.io check that watches the OMNIRA cron
# chain and wires its URLs into /etc/omnira/notify.env for scripts/deadman-ping.sh.
#
# The only input is a healthchecks.io READ-WRITE API key, read from a file so it
# never appears in a command line, a log or this chat:
#   /etc/omnira/healthchecks.env   (mode 0600)   HC_API_KEY=<key>
# Create the key at healthchecks.io -> your project -> Settings -> API Access.
#
# What it does:
#   1. POST /api/v3/checks/ with unique=["name"], so re-running finds the existing
#      check instead of creating a duplicate. Period 5 min, grace 10 min, all of the
#      project's alert channels attached.
#   2. Writes DEADMAN_URL=<ping url> and DEADMAN_FAIL_URL=<ping url>/fail into the
#      env file (replacing old values, keeping every other line, mode 0600).
#   3. Runs scripts/deadman-ping.sh once and prints its state line (never a URL).
#
# Usage: scripts/setup-deadman-healthchecks.sh [--key-file F] [--env-file F] [--api-url U] [--name N] [--no-ping]
set -euo pipefail
cd "$(dirname "$0")/.."

KEY_FILE=/etc/omnira/healthchecks.env
ENV_FILE=/etc/omnira/notify.env
API_URL=https://healthchecks.io
NAME="OMNIRA cron chain"
DO_PING=1
while [ $# -gt 0 ]; do
  case "$1" in
    --key-file) KEY_FILE=$2; shift 2 ;;
    --env-file) ENV_FILE=$2; shift 2 ;;
    --api-url) API_URL=${2%/}; shift 2 ;;
    --name) NAME=$2; shift 2 ;;
    --no-ping) DO_PING=0; shift ;;
    *) echo "unknown option: $1" >&2; exit 2 ;;
  esac
done

[ -r "$KEY_FILE" ] || { echo "cannot read $KEY_FILE (expected a line HC_API_KEY=<read-write key>)" >&2; exit 2; }
HC_API_KEY=$(sed -n 's/^HC_API_KEY=//p' "$KEY_FILE" | tail -1 | tr -d "\"' \r")
[ -n "$HC_API_KEY" ] || { echo "HC_API_KEY is empty in $KEY_FILE" >&2; exit 2; }

body=$(NAME="$NAME" python3 - <<'EOF'
import json, os
print(json.dumps({
    "name": os.environ["NAME"],
    "tags": "omnira cron",
    "desc": "Pinged every 5 min by scripts/deadman-ping.sh only while nats, waha, backup-cloud-check and the hourly backup are all alive.",
    "timeout": 300,
    "grace": 600,
    "unique": ["name"],
    "channels": "*",
}))
EOF
)

resp=$(mktemp)
trap 'rm -f "$resp"' EXIT
# The key travels in a header file, not in argv.
hdr=$(mktemp)
trap 'rm -f "$resp" "$hdr"' EXIT
chmod 600 "$hdr"
printf 'X-Api-Key: %s\n' "$HC_API_KEY" > "$hdr"
code=$(curl -sS -o "$resp" -w '%{http_code}' --connect-timeout 10 --max-time 30 \
  -H "@$hdr" -H 'Content-Type: application/json' --data-binary "$body" "$API_URL/api/v3/checks/") || {
  echo "healthchecks API unreachable" >&2; exit 1; }
case "$code" in
  200|201) ;;
  401|403) echo "healthchecks rejected the API key (HTTP $code): use a READ-WRITE key" >&2; exit 1 ;;
  *) echo "healthchecks API error: HTTP $code" >&2; exit 1 ;;
esac

ping_url=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1])).get("ping_url",""))' "$resp")
case "$ping_url" in
  http*://*) ;;
  *) echo "API answered without a ping_url" >&2; exit 1 ;;
esac
[ "$code" = 201 ] && echo "check created: $NAME" || echo "check already existed, reusing: $NAME"

# Rewrite the file IN PLACE: /etc/omnira is usually root-owned (not writable by us),
# while notify.env itself belongs to the service user, so a temp file next to it
# cannot be created. Building the new content in /tmp first means a failure never
# leaves a half-written file; `cat >` keeps the owner and mode of the original.
umask 077
[ -e "$ENV_FILE" ] || touch "$ENV_FILE"
tmp=$(mktemp)
trap 'rm -f "$resp" "$hdr" "$tmp"' EXIT
grep -vE '^(DEADMAN_URL|DEADMAN_FAIL_URL)=' "$ENV_FILE" > "$tmp" || true
{
  printf 'DEADMAN_URL="%s"\n' "$ping_url"
  printf 'DEADMAN_FAIL_URL="%s/fail"\n' "$ping_url"
} >> "$tmp"
cat "$tmp" > "$ENV_FILE"
chmod 600 "$ENV_FILE"
echo "DEADMAN_URL / DEADMAN_FAIL_URL written to $ENV_FILE (0600)"

if [ "$DO_PING" = 1 ]; then
  echo "== first heartbeat"
  NOTIFY_ENV_FILE="$ENV_FILE" scripts/deadman-ping.sh || true
fi
