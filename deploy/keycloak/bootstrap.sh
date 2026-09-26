#!/usr/bin/env bash
# Keycloak bootstrap: configure omnira-web client secret, set the demo
# user's password, and verify the realm. Neither credential is ever
# committed — omnira-realm.json seeds the demo user WITHOUT a usable
# credential; this script is the only place either secret is generated.
# Usage: ./bootstrap.sh [--admin-url http://localhost:8080] [--client-secret SECRET]
# If no client secret is provided, generates a random one.
# Outputs: CLIENT_ID, CLIENT_SECRET (base64), DEMO_PASSWORD — ready for .env
# injection / local pilot use. Never paste this output into a commit, issue,
# or chat log.

set -uo pipefail

ADMIN_URL="${1:-http://localhost:8888}"
ADMIN_USER="${KEYCLOAK_ADMIN:-admin}"
ADMIN_PASSWORD="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
CLIENT_SECRET="${2:-}"
DEMO_PASSWORD="${DEMO_PASSWORD:-}"

# Generate random secret if not provided
if [ -z "$CLIENT_SECRET" ]; then
  CLIENT_SECRET=$(openssl rand -base64 32)
  echo "Generated client secret: $CLIENT_SECRET"
fi

# Generate a random demo password if not provided. Prefixed to deterministically
# satisfy the realm's password policy (upperCase(1) and lowerCase(1) and
# digits(1) and length(8)) regardless of what the random tail contains.
if [ -z "$DEMO_PASSWORD" ]; then
  DEMO_PASSWORD="Aa1$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 16)"
  echo "Generated demo user password: $DEMO_PASSWORD"
fi

# Wait for Keycloak to be ready
echo "Waiting for Keycloak..."
for i in {1..30}; do
  if curl -sf "$ADMIN_URL/health/ready" >/dev/null 2>&1; then
    echo "Keycloak ready"
    break
  fi
  sleep 2
done

# Get admin token
echo "Authenticating..."
TOKEN=$(curl -s -X POST "$ADMIN_URL/realms/master/protocol/openid-connect/token" \
  -H "Content-Type: application/x-www-form-urlencoded" \
  -d "client_id=admin-cli" \
  -d "username=$ADMIN_USER" \
  -d "password=$ADMIN_PASSWORD" \
  -d "grant_type=password" | jq -r '.access_token')

if [ -z "$TOKEN" ] || [ "$TOKEN" = "null" ]; then
  echo "Failed to authenticate" >&2
  exit 1
fi

# Get client ID
echo "Fetching omnira-web client..."
CLIENT_ID=$(curl -s "$ADMIN_URL/admin/realms/omnira/clients?clientId=omnira-web" \
  -H "Authorization: Bearer $TOKEN" | jq -r '.[0].id')

if [ -z "$CLIENT_ID" ] || [ "$CLIENT_ID" = "null" ]; then
  echo "Client not found" >&2
  exit 1
fi

# Update client secret
echo "Updating client secret..."
curl -s -X PUT "$ADMIN_URL/admin/realms/omnira/clients/$CLIENT_ID" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"secret\": \"$CLIENT_SECRET\"}" >/dev/null

# Verify update
echo "Verifying..."
VERIFIED=$(curl -s "$ADMIN_URL/admin/realms/omnira/clients/$CLIENT_ID/client-secret" \
  -H "Authorization: Bearer $TOKEN" | jq -r '.value')

if [ "$VERIFIED" = "$CLIENT_SECRET" ]; then
  echo "✓ Client secret updated successfully"
else
  echo "Failed to verify secret" >&2
  exit 1
fi

# Set the demo user's password (the realm import seeds the user WITHOUT a
# credential — this is the only place its password is ever set).
echo "Fetching demo user..."
USER_ID=$(curl -s "$ADMIN_URL/admin/realms/omnira/users?username=demo@omnira.local" \
  -H "Authorization: Bearer $TOKEN" | jq -r '.[0].id')

if [ -z "$USER_ID" ] || [ "$USER_ID" = "null" ]; then
  echo "Demo user not found" >&2
  exit 1
fi

echo "Setting demo user password..."
curl -s -X PUT "$ADMIN_URL/admin/realms/omnira/users/$USER_ID/reset-password" \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d "{\"type\": \"password\", \"value\": \"$DEMO_PASSWORD\", \"temporary\": false}" \
  -o /dev/null -w "reset-password status: %{http_code}\n"

echo ""
echo "Add to .env:"
echo "OMNIRA_AUTH_CLIENT_ID=omnira-web"
echo "OMNIRA_AUTH_CLIENT_SECRET=$CLIENT_SECRET"
echo "OMNIRA_AUTH_ISSUER=http://localhost:8888/realms/omnira"
echo ""
echo "Demo login (local pilot use only — never commit or share):"
echo "  demo@omnira.local / $DEMO_PASSWORD"
