# OMNIRA — Instruções de Login

## Credenciais de Teste

Use um dos emails abaixo para fazer login:

| Email | Senha | Tenant | Acesso |
|-------|-------|--------|--------|
| **test@omnira.local** | (qualquer senha) | Test Company LTDA | Administrator |
| **admin@omnira.local** | (qualquer senha) | Test Company LTDA | Administrator |

## Como Fazer Login

### Via Frontend Web

1. Acesse: **http://omnira.devops.k3gsolutions.com.br/login**

2. Digite um dos emails acima

3. Digite qualquer senha (será ignorada em modo teste)

4. Clique em **Entrar**

O sistema irá:
- Fazer request para `/api/v1/auth/login`
- Receber um JWT token
- Salvar no localStorage
- Redirecionar para Dashboard

### Via CLI/API

```bash
# 1. Fazer login
curl -X POST http://omnira.devops.k3gsolutions.com.br/api/v1/auth/login \
  -H "Content-Type: application/json" \
  -d '{"email":"test@omnira.local"}'

# Resposta:
{
  "token": "eyJhbGciOiJSUzI1NiIsInR5cCI6IkpXVCJ9...",
  "expires_in": 86400,
  "user": {
    "id": "22222222-2222-2222-2222-222222222222",
    "email": "test@omnira.local",
    "name": "Test User"
  },
  "tenant": {
    "id": "11111111-1111-1111-1111-111111111111",
    "name": "Test Company LTDA"
  }
}

# 2. Usar token nas requisições
curl -H "Authorization: Bearer <token>" \
  http://omnira.devops.k3gsolutions.com.br/api/v1/accounts
```

## Detalhes Técnicos

### JWT Token

O token é um JWT válido com claims:

```json
{
  "sub": "test@omnira.local",
  "user_id": "22222222-2222-2222-2222-222222222222",
  "tenant_id": "11111111-1111-1111-1111-111111111111",
  "email": "test@omnira.local",
  "iat": 1695120000,
  "exp": 1695206400,
  "iss": "omnira-mock",
  "aud": "omnira-api"
}
```

**Validade**: 24 horas

### Validação

O backend verifica:
- Assinatura RSA
- Token não expirado
- Claims obrigatórios (sub, tenant_id, iat, exp)

### Modo Mock

⚠️ **Este é um endpoint de DESENVOLVIMENTO APENAS**

Em produção, OMNIRA usa:
- OAuth 2.0 / OpenID Connect (recomendado)
- Integração com KeyCloak, Auth0, Google, etc
- Sem endpoint de mock login

Para ativar OIDC:
```env
OMNIRA_OIDC_PROVIDER_URL=https://seu-oidc-provider.com
OMNIRA_OIDC_CLIENT_ID=xxxxx
OMNIRA_OIDC_CLIENT_SECRET=xxxxx
```

## Troubleshooting

### "Email não encontrado"

Use um dos emails da tabela acima. Emails customizados não funcionam no modo mock.

Para adicionar mais usuários, edite `internal/platform/authn/mock_login.go` e recompile:

```bash
cd /data/home-moved/Projects/_legacy_lowercase_projects/20-OMNIRA
go build -o bin/omnira-api cmd/api/main.go
./bin/omnira-api
```

### Token expirado

Tokens duram 24 horas. Para renovar, faça novo login.

### CORS Error

Se receber erro CORS, verifique que:
- Backend está em `localhost:8080`
- Frontend está em `localhost:3000`
- Nginx está em `omnira.devops.k3gsolutions.com.br`
- Headers CORS estão configurados

### Senha rejeitada

Em modo mock, qualquer senha é aceita. Se receber erro, verifique o email.

---

**Status**: 🟢 Login funcionando (modo desenvolvimento)

**Próximo**: Integrar OIDC real ou criar usuários customizados no banco
