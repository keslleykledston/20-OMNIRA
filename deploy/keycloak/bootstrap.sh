#!/usr/bin/env bash
# Keycloak bootstrap: configure omnira-web client secret and verify realm.
# Usage: ./bootstrap.sh [--admin-url http://localhost:8080] [--client-secret SECRET]
# If no client secret is provided, generates a random one.
# Outputs: CLIENT_ID, CLIENT_SECRET (base64), ready for .env injection.

set -uo pipefail

ADMIN_URL="${1:-http://localhost:8888}"
ADMIN_USER="${KEYCLOAK_ADMIN:-admin}"
ADMIN_PASSWORD="${KEYCLOAK_ADMIN_PASSWORD:-admin}"
CLIENT_SECRET="${2:-}"

# Generate random secret if not provided
if [ -z "$CLIENT_SECRET" ]; then
  CLIENT_SECRET=$(openssl rand -base64 32)
  echo "Generated client secret: $CLIENT_SECRET"
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
  echo ""
  echo "Add to .env:"
  echo "OMNIRA_AUTH_CLIENT_ID=omnira-web"
  echo "OMNIRA_AUTH_CLIENT_SECRET=$CLIENT_SECRET"
  echo "OMNIRA_AUTH_ISSUER=http://localhost:8888/realms/omnira"
else
  echo "Failed to verify secret" >&2
  exit 1
fi
