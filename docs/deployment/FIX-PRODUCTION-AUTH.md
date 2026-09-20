# Fix — Production Authentication Error

**Problema**: Plataforma em produção retorna erro "Não foi possível consultar o modo de autenticação"

**Causa**: `OMNIRA_AUTH_MODE=oidc` configurado, mas Keycloak/IdP não acessível

**Rota falhando**: `GET /api/v1/auth/mode` → 404

## Solução Imediata (Habilitar Mock Auth)

Mudança temporária para mock-auth enquanto OIDC é configurado.

### Via Docker (Servidor de Produção)

```bash
# 1. SSH para servidor de produção
ssh user@omnira.devops.k3gsolutions.com.br

# 2. Parar container API
docker stop omnira-api

# 3. Atualizar variável de ambiente
export OMNIRA_AUTH_MODE=mock
docker run -d \
  --name omnira-api \
  -e OMNIRA_AUTH_MODE=mock \
  -e OMNIRA_DATABASE_URL="postgres://omnira_app:password@postgres:5432/omnira_prod?sslmode=disable" \
  -e OMNIRA_NATS_URL="nats://nats:4222" \
  -p 8080:8080 \
  omnira-api:latest

# 4. Verificar se rota funciona
curl -k https://omnira.devops.k3gsolutions.com.br/api/v1/auth/mode
# Esperado: {"mode":"mock"}
```

### Via Docker Compose (Recomendado)

Se usando docker-compose.yml:

```yaml
# Update omnira-api service
api:
  image: omnira-api:latest
  environment:
    OMNIRA_AUTH_MODE: "mock"  # ← Mudança
    OMNIRA_DATABASE_URL: "postgres://omnira_app:${DB_PASSWORD}@postgres:5432/omnira_prod"
    # ... resto
```

Depois:
```bash
docker compose up -d api
```

### Verificar Sucesso

```bash
# 1. Testar rota de auth
curl -k https://omnira.devops.k3gsolutions.com.br/api/v1/auth/mode
# Resposta: {"mode":"mock"}

# 2. Testar login
curl -k -X POST https://omnira.devops.k3gsolutions.com.br/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"admin@omnira.local"}'
# Resposta: {"token":"...", "user":{...}, "tenant":{...}}

# 3. Abrir no browser
open https://omnira.devops.k3gsolutions.com.br/login
# Esperado: Formulário de login mock aparece
```

## Credenciais Mock (Produção)

Login mock padrão funciona:

| Email | Senha | Papel |
|-------|-------|-------|
| `test@omnira.local` | qualquer | Agent |
| `admin@omnira.local` | qualquer | Admin |

(Credenciais mock não usam senha; qualquer valor funciona)

## Próximos Passos (OIDC Real)

Quando IdP (Keycloak/Auth0) estiver disponível:

1. **Provisionar IdP:**
   - URL de issuer: `https://idp.k3gsolutions.com.br/realms/omnira`
   - Cliente: `omnira-web`
   - Secret: (salvo em vault)
   - Redirect: `https://omnira.devops.k3gsolutions.com.br/api/v1/auth/oidc/callback`

2. **Atualizar produção:**
   ```bash
   export OMNIRA_AUTH_MODE=oidc
   export OMNIRA_AUTH_ISSUER="https://idp.k3gsolutions.com.br/realms/omnira"
   export OMNIRA_AUTH_AUDIENCE="omnira-web"
   export OMNIRA_AUTH_CLIENT_ID="omnira-web"
   export OMNIRA_AUTH_CLIENT_SECRET="<vault-secret>"
   export OMNIRA_AUTH_REDIRECT_URL="https://omnira.devops.k3gsolutions.com.br/api/v1/auth/oidc/callback"
   export OMNIRA_AUTH_POST_LOGIN_URL="/login?oidc=complete"
   export OMNIRA_AUTH_COOKIE_SECURE=true
   
   docker compose up -d api
   ```

3. **Verificar:**
   ```bash
   curl -k https://omnira.devops.k3gsolutions.com.br/api/v1/auth/mode
   # Resposta: {"mode":"oidc"}
   ```

## Segurança — Notas

- Mock-auth é **apenas para dev/teste**; não usar em produção crítica
- Sem validação de senha; qualquer email funciona
- Sem MFA
- Para produção real: **OIDC + IdP obrigatório**
- Sessões mock não persistem entre restarts

## Rollback

Se algo der errado:

```bash
# Reverter para versão anterior
docker stop omnira-api
docker run -d \
  --name omnira-api-backup \
  <previous-image>
```

---

**Status**: Documentado. Aguardando execução no servidor de produção.

Após correção, OMNIRA login deve funcionar → continuar com GATE R2.
