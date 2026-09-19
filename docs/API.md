# OMNIRA API Documentation

## Overview

OMNIRA SaaS Platform exposes a RESTful HTTP API for tenant and membership management, along with comprehensive health/metrics endpoints.

**API Version:** 0.1.0 (R0.1 Tenant Foundation)

## Quick Start

### Base URL
- Development: `http://localhost:8080`
- Production: `https://api.omnira.local` (TBD)

### Authentication

All tenant-scoped endpoints accept an OIDC session cookie (`omnira_session`,
HttpOnly) for browser clients or a **Bearer JWT token** in the Authorization
header for API clients:

```bash
Authorization: Bearer <JWT_TOKEN>
```

The JWT token is RSA-signed and OIDC-compatible. Tokens are validated server-side before authorizing access. The mock login is restricted to non-production environments.

### Example Request

```bash
curl -H "Authorization: Bearer $JWT_TOKEN" \
  http://localhost:8080/tenant
```

## OpenAPI Specification

The complete OpenAPI 3.0.0 specification is available at:

**Canonical file:** `contracts/openapi/omnira-v1.yaml`

To view interactively:
1. Copy `contracts/openapi/omnira-v1.yaml` content
2. Paste into [Swagger Editor](https://editor.swagger.io)
3. Explore endpoints, schemas, and examples

## Endpoints

### Health & Observability

#### `GET /healthz`
Comprehensive health check including database and NATS connectivity.

**Response (200 OK - Healthy):**
```json
{
  "status": "healthy",
  "timestamp": "2024-09-18T18:30:00Z",
  "components": {
    "database": {
      "name": "database",
      "status": "healthy",
      "latency_ms": 5
    },
    "nats": {
      "name": "nats",
      "status": "healthy",
      "latency_ms": 2
    }
  }
}
```

**Response (503 Service Unavailable - Unhealthy):**
```json
{
  "status": "unhealthy",
  "timestamp": "2024-09-18T18:30:00Z",
  "components": {
    "database": {
      "name": "database",
      "status": "unhealthy",
      "message": "ping failed: connection refused",
      "latency_ms": 0
    }
  }
}
```

#### `GET /metrics`
Prometheus-format metrics for monitoring and alerting.

**Response (200 OK):**
```
# HELP omnira_health_status Health status of components (1=healthy, 0=unhealthy)
# TYPE omnira_health_status gauge
omnira_health_status{component="database"} 1
omnira_health_status{component="nats"} 1

# HELP omnira_health_latency_ms Latency of health check in milliseconds
# TYPE omnira_health_latency_ms gauge
omnira_health_latency_ms{component="database"} 5
omnira_health_latency_ms{component="nats"} 2
```

### Tenancy

#### `GET /tenant`
Retrieve the current tenant for the authenticated user.

**Headers:** `Authorization: Bearer <JWT_TOKEN>`

**Response (200 OK):**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "legal_name": "Acme Corp",
  "isolation_profile": "standard",
  "status": "active",
  "created_at": "2024-09-01T10:00:00Z"
}
```

**Error Responses:**
- `401 Unauthorized` — Missing or invalid JWT token
- `403 Forbidden` — User not authorized for any tenant

#### `GET /tenant/memberships`
List all memberships in the current tenant.

**Headers:** `Authorization: Bearer <JWT_TOKEN>`

**Response (200 OK):**
```json
{
  "memberships": [
    {
      "id": "550e8400-e29b-41d4-a716-446655440001",
      "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
      "user_id": "550e8400-e29b-41d4-a716-446655440002",
      "role_id": "admin",
      "status": "active",
      "created_at": "2024-09-01T10:00:00Z"
    }
  ]
}
```

#### `POST /tenant/memberships/{userID}`
Grant a user membership in the current tenant.

**Headers:** `Authorization: Bearer <JWT_TOKEN>`

**Path Parameters:**
- `userID` (UUID) — User to grant membership to

**Request Body:**
```json
{
  "role_id": "viewer"
}
```

**Response (201 Created):**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440001",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "user_id": "550e8400-e29b-41d4-a716-446655440002",
  "role_id": "viewer",
  "status": "active",
  "created_at": "2024-09-18T18:30:00Z"
}
```

#### `DELETE /tenant/memberships/{userID}`
Revoke a user's membership in the current tenant.

**Headers:** `Authorization: Bearer <JWT_TOKEN>`

**Path Parameters:**
- `userID` (UUID) — User to revoke

**Response (204 No Content)**

**Error Responses:**
- `404 Not Found` — Membership not found
- `403 Forbidden` — Not authorized to revoke

## Security

### Authentication & Authorization

1. **JWT Validation:**
   - RSA signature verification
   - Token expiry check
   - OIDC-compatible format

2. **Authorization:**
   - Tenant context extracted after authentication
   - Row-level security (RLS) enforced at database level
   - Never trust `tenant_id` from request payload

3. **Isolation:**
   - All tenant-owned data protected by RLS policies
   - Cross-tenant access attempts blocked at DB level
   - Audit trail logged for all operations

### HTTPS (Production)

All production endpoints must use HTTPS with valid certificates.
HTTP is only permitted in development.

## Error Handling

All error responses follow this format:

```json
{
  "error": "Unauthorized",
  "message": "Invalid or expired token",
  "code": "AUTH_ERROR"
}
```

**Common Status Codes:**
- `200 OK` — Success
- `201 Created` — Resource created
- `204 No Content` — Success (no body)
- `400 Bad Request` — Invalid input
- `401 Unauthorized` — Missing/invalid credentials
- `403 Forbidden` — Authorized but no permission
- `404 Not Found` — Resource not found
- `500 Internal Server Error` — Server error
- `503 Service Unavailable` — Dependency down (DB, NATS)

## Rate Limiting

Rate limiting is not yet implemented in R0.1.
This will be added in R0.2.

## Versioning

The API uses URL versioning in future releases:
- `v1` — Current (R0.1)
- `v2` — Future (R0.2+)

## OpenAPI Tooling

**Swagger UI** (view specs in browser):
```bash
# Using Docker
docker run -p 8001:8080 \
  -e SWAGGER_JSON=/docs/api/openapi.yaml \
  -v $(pwd)/docs:/docs \
  swaggerapi/swagger-ui
```

**Redoc** (alternative UI):
```bash
docker run -p 8002:80 \
  -v $(pwd)/docs:/usr/share/nginx/html/docs \
  redocly/redoc
```

## Related Documentation

- **Event API:** See [`docs/async/asyncapi.yaml`](../async/asyncapi.yaml)
- **Architecture:** See [`docs/architecture/`](../architecture/)
- **Deployment:** See [`docs/architecture/DEPLOYMENT-NGINX.md`](../architecture/DEPLOYMENT-NGINX.md)
