# Arquitetura de Identidade e Autenticação

**Versão:** 1.0 (Phase 21-22)  
**Status:** Produção  
**Responsável:** Engineering Team

## Visão Geral

OMNIRA separa **autenticação** (comprovação de identidade via IdP) de **autorização** (memberships em Tenants).

```
IdP (Keycloak)
    ↓ autentica
    ↓ emite ID Token (sub, email, name)
    ↓
OMNIRA Backend
    ↓ valida token (RS256, issuer, nonce, exp)
    ↓ mapeia sub → UserIdentity → User
    ↓ cria sessão HttpOnly
    ↓
Browser Session
    ↓ cookie omnira_session (ID Token)
    ↓ API calls include cookie
    ↓
OMNIRA Backend
    ↓ valida sessão
    ↓ resolve User → Memberships → TenantContext
    ↓ RLS aplica autorização
    ↓
Dados do Tenant
    ↓
Frontend recebe autorizado
```

## Componentes Principais

### 1. Autenticação (Browser)

**Fluxo:**

```
GET /login
    ↓
GET /api/v1/auth/oidc/start
    ↓ backend cria state/nonce/PKCE, salva em cookies
    ↓ redirect Keycloak
    ↓
Keycloak: authenticate user
    ↓
GET /api/v1/auth/oidc/callback?code=...&state=...
    ↓ backend valida state
    ↓ exchange code para ID Token
    ↓ valida token (RS256, issuer, nonce, exp)
    ↓ resolve/provision UserIdentity
    ↓ set omnira_session cookie
    ↓ redirect ?oidc=complete
    ↓
GET /login?oidc=complete
    ↓ frontend detecta
    ↓ GET /api/v1/auth/session
    ↓ resolve User → Tenant
    ↓ navigate /
```

**Security:**

- State: 32 bytes random, constant-time comparison
- Nonce: 32 bytes random, bound to ID Token
- PKCE: S256, 48 bytes verifier
- Cookies: 10-min expiry (auth transaction)
- Session: HttpOnly + Secure (prod) + SameSite=Lax

### 2. Sessão de Aplicação

**Cookie `omnira_session`:**

- Valor: ID Token (JWS)
- HttpOnly: true (JavaScript não acessa)
- Secure: true (HTTPS only, production)
- SameSite: Lax (CSRF protection)
- Path: /
- MaxAge: até expiração do ID Token (typ. 5 min)

**Fluxo:**

```
POST /api/v1/tenants/{id}/inbox/conversations
    ↓ headers: no Authorization (browser usa cookie)
    ↓ withCredentials: true (axios automático)
    ↓
Backend:
    ↓ extract cookie omnira_session
    ↓ parse JWT (sem validação, confia na própria assinatura)
    ↓ read sub
    ↓ query User by external_subject=sub
    ↓ injetar Principal no context
    ↓
Middleware AuthZ:
    ↓ query Memberships
    ↓ injetar TenantContext
    ↓
RLS:
    ↓ SET app.tenant_id = context.tenant_id
    ↓ queries retornam dados filtrados
```

### 3. Identidade Local (UserIdentity)

**Tabela:**

```sql
user_identities(
  id UUID PRIMARY KEY,
  user_id UUID → users.id,
  issuer TEXT,
  subject TEXT,
  email TEXT,
  display_name TEXT,
  created_at TIMESTAMPTZ,
  last_login_at TIMESTAMPTZ,
  UNIQUE(issuer, subject)
)
```

**Lógica:**

- **Chave de identidade:** (issuer, subject)
- **Email:** atributo, não chave
- **First login:** JIT provisioning (cria User + UserIdentity)
- **Repeat login:** update email/display_name, last_login_at
- **Diferentes issuers:** diferentes Users (mesmo subject OK)

**Reconciliação:**

```
OIDC token sub="user-123" issuer="https://idp/realms/omnira"
    ↓
Busca UserIdentity(issuer, sub)
    ↓
Encontrada:
    ↓ retorna user_id
    ✓ já existe
    
Não encontrada:
    ↓ cria User (novo)
    ↓ cria UserIdentity
    ✓ JIT provisioning
```

## Configuração

### Env Vars

```bash
# Modo: mock (dev/test), oidc (staging/production)
OMNIRA_AUTH_MODE=oidc

# OIDC Discovery (issuer)
OMNIRA_AUTH_ISSUER=https://keycloak.internal/realms/omnira

# OIDC Client
OMNIRA_AUTH_AUDIENCE=omnira-web
OMNIRA_AUTH_CLIENT_ID=omnira-web
OMNIRA_AUTH_CLIENT_SECRET=<from-keycloak-console>

# Callback URL (deve estar cadastrada no IdP)
OMNIRA_AUTH_REDIRECT_URL=https://app.omnira.k3g.internal/api/v1/auth/oidc/callback

# Pós-login (frontend)
OMNIRA_AUTH_POST_LOGIN_URL=/

# Cookie segurança
OMNIRA_AUTH_COOKIE_SECURE=true  # production
OMNIRA_AUTH_COOKIE_SECURE=false # dev

# Ambiente
OMNIRA_ENV=production
```

### Production Checklist

- [ ] `OMNIRA_ENV=production`
- [ ] `OMNIRA_AUTH_MODE=oidc`
- [ ] `OMNIRA_AUTH_ISSUER=https://...` (HTTPS)
- [ ] `OMNIRA_AUTH_REDIRECT_URL=https://...` (HTTPS)
- [ ] `OMNIRA_AUTH_CLIENT_SECRET` (de secret manager, não .env)
- [ ] `OMNIRA_AUTH_COOKIE_SECURE=true`
- [ ] Keycloak TLS configurado (KC_PROXY=edge, KC_HOSTNAME, etc)
- [ ] Nginx proxy: X-Forwarded-* headers configurados
- [ ] Backup/restore Keycloak testado
- [ ] Health checks funcionando

## Testes

### E2E

```bash
# Desenvolvimento (mock)
OMNIRA_AUTH_MODE=mock go test ./...

# Staging (OIDC real)
docker-compose up -d keycloak keycloak-postgres
export OMNIRA_AUTH_ISSUER=http://localhost:8888/realms/omnira
export OMNIRA_AUTH_MODE=oidc
go test ./...
web: npm test
```

### Security

```bash
# Fail-closed: staging + mock
OMNIRA_ENV=staging OMNIRA_AUTH_MODE=mock go run ./apps/api/cmd/omnira-api
# Expected: boot fail with clear error

# HTTPS enforcement: prod + http
OMNIRA_ENV=production OMNIRA_AUTH_ISSUER=http://... go run ./apps/api/cmd/omnira-api
# Expected: boot fail
```

### Cobertura

- State/nonce/PKCE validation
- Token expiry
- Wrong issuer rejection
- Unknown identity (user not provisioned)
- User without membership (403, no auto-grant)
- Cookie flags (HttpOnly, Secure, SameSite)
- Logout clears session
- Open redirect blocked
- Replay protection (state consumed)

## Falhas e Recuperação

### Keycloak Indisponível

**Scenario:** API running, Keycloak offline

**Resultado:**
- Novo login: `GET /auth/oidc/start` falha
- Sessão existente: API continua funcionando (OIDC não consultado)
- Reauth: usuário precisa esperar Keycloak voltar

**Mitigation:**
- Keycloak HA (não nesta fase)
- Health checks monitoram Keycloak
- Oncall alertado

### Token Expirado

**Scenario:** Usuário está usando OMNIRA, token expira

**Resultado:**
- Próximo request: middleware valida cookie
- Token inválido: remove cookie, retorna 401
- Frontend: redireciona /login
- Novo login: nova sessão

**Sem refresh token:**
- User re-autentica (simples, seguro)

### Senha Muda no IdP

**Scenario:** Admin reset no Keycloak

**Resultado:**
- Sessão OMNIRA: continua até expiração de token
- Keycloak: novo token tem novo password hash
- Next login: autentica com nova senha
- Sem re-auth até expiração: OK

### Membership Revogado

**Scenario:** Admin remove user de Tenant

**Resultado:**
- Sessão OMNIRA: cookie ainda válido
- Próximo request: query Memberships retorna vazio
- RLS: acesso negado (no row access)
- Frontend: vê 403, mostra "sem acesso"
- Sem auto-re-login: revogação imediata

## Segurança

### Defesas

| Ameaça | Defesa |
|--------|--------|
| XSS token theft | HttpOnly cookie (JS não lê) |
| CSRF | SameSite=Lax cookie |
| Session fixation | state + nonce único por flow |
| Replay | state consumido uma vez |
| PKCE bypass | S256 (server-side comparison) |
| Token forge | RS256 signature validation |
| Wrong issuer | issuer header validation |
| Expired token | exp claim validation |
| Wrong audience | aud claim validation |
| Open redirect | redirect_uri exact match |
| Password reuse | IdP gerencia (Keycloak) |
| Timing attack | constant-time comparison (state, nonce) |

### Não Cobertas (Escopo Futuro)

- Refresh token (usuário re-autentica a cada expiração)
- Single logout do IdP (logout local apenas)
- Multi-factor authentication (configurar no Keycloak)
- Session revocation (no server-side session store yet)

## Operação

### Deploy

```bash
# 1. Database migration
./tools/migrate-sql.sh up

# 2. Keycloak setup (uma vez)
docker-compose up -d keycloak keycloak-postgres
./deploy/keycloak/bootstrap.sh

# 3. Environment (secret manager)
export OMNIRA_AUTH_CLIENT_SECRET=<from-keycloak>
export OMNIRA_AUTH_ISSUER=<from-bootstrap>

# 4. API start
docker-compose up -d api worker web

# 5. Verificar
curl -s https://app/api/v1/auth/mode  # deve retornar {"mode":"oidc"}
curl -s https://app/api/v1/healthz    # health check
```

### Monitoramento

```bash
# Health
curl -s https://app/api/v1/healthz | jq .

# Keycloak
curl -s http://keycloak:8080/health/ready | jq .

# Logs
docker logs omnira-api | grep -i auth
docker logs omnira-keycloak | grep -i error
```

### Troubleshooting

**Login falha: "identity not provisioned"**
- User não tem UserIdentity no OMNIRA
- Admin deve criar User + provision
- Ou: usar Keycloak admin console para reset

**Cookie não é enviado**
- withCredentials não está ativo em axios
- Verificar web/src/lib/api.ts

**HTTPS complaint: mixed content**
- Frontend HTTPS, Keycloak HTTP
- Configurar KC_PROXY=edge, KC_HOSTNAME

## Referências

- [ADR-0010: OIDC e sessão HttpOnly](../adr/0010-oidc-browser-session.md)
- [KEYCLOAK.md: Setup e operação](../ops/KEYCLOAK.md)
- [OpenID Connect Core 1.0](https://openid.net/specs/openid-connect-core-1_0.html)
- [RFC 7636: PKCE](https://www.rfc-editor.org/rfc/rfc7636.html)
