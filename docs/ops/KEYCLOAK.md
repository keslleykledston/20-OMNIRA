# Keycloak — Self-hosted OIDC IdP para OMNIRA

**Versão:** Keycloak 25.0 (quay.io/keycloak/keycloak:25.0)

**Banco:** PostgreSQL 16 (separado, container `omnira-keycloak-postgres`)

**Realms:** 1 realm (`omnira`), N usuários

## Visão Geral

OMNIRA usa Keycloak como referência de provedor OIDC (OpenID Connect) self-hosted. O Keycloak autentica usuários; OMNIRA autoriza acesso via Tenant memberships (RLS).

**Fluxo de autenticação:**

```
Browser
  ↓ GET /auth/login
OMNIRA Backend
  ↓ Create state/nonce/PKCE, redirect
Keycloak
  ↓ Authenticate user
  ↓ Redirect /auth/oidc/callback?code=...&state=...
OMNIRA Backend
  ↓ Exchange code for ID Token
  ↓ Validate token (RS256, issuer, nonce, expiry)
  ↓ Reconcile sub com users.external_subject
  ↓ Create session (HttpOnly cookie omnira_session)
  ↓ Redirect app
Browser
  ↓ GET /api/v1/me
OMNIRA Backend
  ↓ Read session, resolve Memberships → TenantContext
  ↓ Return authorized tenant
```

## Setup Local (docker-compose)

### 1. Iniciar stack

```bash
cp .env.example .env
# Editar .env:
#   KEYCLOAK_ADMIN_PASSWORD=your_password
#   KEYCLOAK_DB_PASSWORD=your_db_password
#   OMNIRA_AUTH_MODE=oidc (se quer testar com Keycloak real)

docker-compose up -d
docker-compose logs -f keycloak
```

Keycloak está pronto quando:
```
[org.jboss.as.server] (Controller Boot Thread) WFLC0013: Stopped in 14ms - Server is now stopped
INFO  [org.keycloak.services.managers.ApplianceBootstrap] (main)
    Keycloak 25.0 started
```

### 2. Verificar realm e client

Acesse admin console:

```
http://localhost:8888/admin/
Login: admin / (password from KEYCLOAK_ADMIN_PASSWORD)
Navigate: Realms → omnira → Clients → omnira-web
```

**omnira-web client debe estar:**
- Type: Confidential
- Client authentication: Enabled
- Redirect URIs: http://localhost:8080/api/v1/auth/oidc/callback, http://localhost:3000/login?oidc=complete
- Scopes: openid, email, profile

### 3. Obter client secret

```bash
# Via bootstrap script (recomendado):
./deploy/keycloak/bootstrap.sh

# Output:
# OMNIRA_AUTH_CLIENT_ID=omnira-web
# OMNIRA_AUTH_CLIENT_SECRET=<random>
# OMNIRA_AUTH_ISSUER=http://localhost:8888/realms/omnira

# Adicionar ao .env:
cat >> .env <<'EOF'
OMNIRA_AUTH_CLIENT_ID=omnira-web
OMNIRA_AUTH_CLIENT_SECRET=<output do bootstrap>
OMNIRA_AUTH_MODE=oidc
OMNIRA_AUTH_ISSUER=http://localhost:8888/realms/omnira
EOF

# Reiniciar API/worker:
docker-compose restart api worker
```

### 4. Criar usuário de teste

Admin console → Realms → omnira → Users → Create user

Ou use o usuário demo já provisionado:
```
Username: demo@omnira.local
Password: demo123
```

**Mas:** Usuário no Keycloak NÃO aparecerá automaticamente em OMNIRA. Você precisa:

1. Provisionar manualmente em OMNIRA (`INSERT INTO users` com `external_subject=sub` do Keycloak)
2. Criar membership (associar à tenant)

**Depois** o login OIDC criará/atualizará a identidade (UserIdentity model).

### 5. Testar login OIDC

```bash
# Assumindo OMNIRA_AUTH_MODE=oidc no .env

# Abrir browser:
http://localhost:3000

# Clicar "Login"
# Ser redirecionado para Keycloak
# Authenticate com demo@omnira.local / demo123
# Retornar para OMNIRA
```

Se falhar: verificar logs

```bash
docker-compose logs api  # OMNIRA error logs
docker-compose logs keycloak  # Keycloak logs
```

## Database Separation

**Keycloak Database:**
```
Host: keycloak-postgres:5432 (container)
User: keycloak
Password: (KEYCLOAK_DB_PASSWORD)
Database: keycloak
Port (localhost): 127.0.0.1:55435
```

**OMNIRA Database:**
```
Host: postgres:5432 (container)
User: omnira (owner), omnira_app (runtime)
Password: (POSTGRES_PASSWORD)
Database: omnira_dev
Port (localhost): 127.0.0.1:55434
```

**PostgreSQL role restrictions:**
- `keycloak` role: acesso SOMENTE à database `keycloak`
- `omnira_app` role: acesso SOMENTE à database `omnira_dev`

Cross-database queries não são permitidas.

## Realm Configuration

**omnira-realm.json** (versionado em deploy/keycloak/):
- Realm name: omnira
- Token lifespan: 300s (5 min)
- Refresh token lifespan: 86400s (24h)
- Demo user: demo@omnira.local / demo123

**Client omnira-web:**
- Confidential flow
- Authorization Code + PKCE
- Redirect URIs (dev + prod):
  - http://localhost:8080/api/v1/auth/oidc/callback
  - http://localhost:3000/login?oidc=complete
  - https://omnira.devops.k3gsolutions.com.br/api/v1/auth/oidc/callback

## Backup e Restore

### Backup

```bash
# Backup Keycloak database
docker exec omnira-keycloak-postgres pg_dump -U keycloak keycloak > keycloak_backup.sql

# Backup realm export (via admin API)
curl -s http://localhost:8888/admin/realms/omnira \
  -H "Authorization: Bearer $TOKEN" | jq . > keycloak_realm_export.json
```

### Restore

```bash
# Restore database
docker exec -i omnira-keycloak-postgres psql -U keycloak -d keycloak < keycloak_backup.sql

# Restart Keycloak
docker-compose restart keycloak
```

## Production Hardening

### SSL/TLS

Em produção, Keycloak DEVE estar atrás de proxy com TLS:

```bash
# No Keycloak (environment):
KC_PROXY=edge                               # Trust X-Forwarded-* headers
KC_HOSTNAME=auth.omnira.internal           # Public hostname (issuer)
KC_HOSTNAME_STRICT=true                    # Require exact match
KC_HTTP_ENABLED=false                      # Disable plain HTTP
```

### Admin Password

Mude da senha padrão:

```bash
# Via admin API
curl -X POST http://localhost:8888/admin/realms/master/users/{USER_ID}/reset-password \
  -H "Authorization: Bearer $ADMIN_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "password",
    "value": "new_secure_password",
    "temporary": false
  }'
```

### Realm Export Strategy

Não confie em export manual. Versione:
- deploy/keycloak/omnira-realm.json (metadados de cliente)
- Client secret por bootstrap ou secret manager
- Users provision via migration OMNIRA (não Keycloak export)

### Monitoring

Keycloak logs em `docker-compose logs keycloak`.

Métricas (se adicionadas):
- Login success/failure rate
- Token validation errors
- Database connection pool

## Troubleshooting

### "Keycloak not ready"

```bash
docker-compose logs keycloak | grep ERROR

# Causa comum: database connection
docker-compose logs keycloak-postgres

# Solução: Reiniciar tudo
docker-compose down
docker-compose up -d
```

### "Client secret not found"

```bash
# Verificar se client existe
curl http://localhost:8888/admin/realms/omnira/clients?clientId=omnira-web \
  -H "Authorization: Bearer $TOKEN"

# Se não existe, reimportar realm
docker-compose restart keycloak  # Usa omnira-realm.json
```

### "Redirect URI mismatch"

No admin console:
1. Realms → omnira → Clients → omnira-web
2. Valid Redirect URIs: verificar se URI atual está na lista
3. Atualizar e salvar

### "Token validation failed"

```bash
# Verificar logs OMNIRA
docker-compose logs api | grep "oidc\|verify"

# Causa comum: issuer mismatch
# OMNIRA_AUTH_ISSUER deve corresponder ao issuer no ID Token
# Keycloak ISSUER = KC_HOSTNAME + /realms/omnira
```

## Próximos Passos

- [ ] AUTH.2: Backend endpoints (login/callback/logout)
- [ ] AUTH.3: Local identity mapping (UserIdentity model)
- [ ] AUTH.4: Frontend integration
- [ ] AUTH.5: Production hardening (rate limiting, telemetry)
- [ ] AUTH.6: E2E tests, docker smoke, ADR final

## Referências

- [Keycloak Docs](https://www.keycloak.org/documentation)
- [OpenID Connect Core](https://openid.net/specs/openid-connect-core-1_0.html)
- [ADR-0010: OIDC Browser Session](../adr/0010-oidc-browser-session.md)
