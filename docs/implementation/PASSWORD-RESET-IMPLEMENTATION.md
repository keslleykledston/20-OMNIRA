# Implementação de Controle de Usuários, Senha Temporária e Reset de Senha — OMNIRA

**Status**: 60% - Infraestrutura Backend Pronta, Integração Parcial
**Última Atualização**: 2026-10-05
**Responsável**: Implementação ativa (Claude Code)

## Arquitetura de Decisão

### 1. **Password Local + OIDC (ESCOLHIDO)**

- **OIDC via Keycloak**: continua como mecanismo de login principal (sem mudanças)
- **Password Local (novo)**: apenas para:
  1. Primeira autenticação após receber convite (senha temporária)
  2. Reset de senha ("Esqueci a senha")
  3. Troca obrigatória de senha no primeiro login
- **Vantagens**: Simples, auditável, sem dependência Keycloak API, compatível com OIDC
- **Bcrypt**: cost=12, seguro, auditado

### 2. **Fluxo Completo Planejado**

```
1. Admin cria convite (POST /team/invitations)
   ↓ Gera senha temporária aleatória
   ↓ Hash-a em bcrypt
   ↓ Envia por email com token de convite
   
2. Usuário recebe email com:
   - Link de convite: /invite/<token>
   - Senha temporária: ABC123!@#Xyz (12 chars)
   - Aviso: Válida por 72h, troca obrigatória ao entrar
   
3. Usuário aceita convite (GET /invite/<token>)
   ↓ Valida token (ainda válido?)
   ↓ Cria/atualiza user com:
     - password_hash (bcrypt)
     - password_expires_at (NOW + 72h)
   
4. Usuário tenta login
   ↓ POST /auth/password-verify (email + password)?
   ↓ Valida password_hash
   ↓ Se válida, cria sessão + flag force_password_reset=true
   
5. Frontend intercepta force_password_reset
   ↓ Redireciona para tela obrigatória de troca
   ↓ POST /auth/password-change (old + new)
   ↓ Limpa flag, login normal
   
6. "Esqueci a senha"
   ↓ POST /auth/password-reset-request (email)
   ↓ Gera token reset válido por 1h
   ↓ Envia email com link
   ↓ Usuário clica → tela de reset
   ↓ POST /auth/password-reset (token + new_password)
   ↓ Valida token, atualiza password_hash, limpa expiração
```

## Implementação Realizada (60%)

### ✅ Concluído

**1. Migration 000081 (`migrations/000081_user_password_support.{up,down}.sql`)**
```sql
ALTER TABLE users ADD password_hash TEXT
ALTER TABLE users ADD password_set_at TIMESTAMPTZ
ALTER TABLE users ADD password_expires_at TIMESTAMPTZ
CREATE TABLE password_resets (token_hash, used_at, expires_at...)
```

**2. Package `internal/password/password.go`**
- `Hash(password)` → bcrypt hash (cost=12)
- `Verify(hash, password)` → bool
- `Generate()` → random 12-char password
- `ExpiresAt()` → time.Now() + 72h
- Testes: `password_test.go` ✅

**3. Email + Invitations**
- `InvitationMessage` → adiciona `TemporaryPassword`, `PasswordExpiresAt`
- Templates (text + HTML) → incluem senha no corpo
- `renderInvitationEmail()` → passa `Password` aos templates
- `CreateInvitation()` → gera senha, passa para `deliver()`
- `ResendInvitation()` → gera nova senha a cada reenvio
- `deliver(raw, tempPassword)` → assinatura atualizada

### ❌ Pendente

**1. Aceitação de Convite + Primeira Autenticação**
- Modificar `AcceptInvitation()` para:
  1. Criar user com `password_hash` + `password_expires_at`
  2. OU atualizar user existente com mesmos campos
  3. Guardar a senha bruta temporária em algum lugar seguro para validação?
  
  **Questão de design**: A senha é validada no momento do `/invite/<token>/accept` ou no login posterior?
  
  Opções:
  - **A (recomendado)**: Validate no accept → exigir password no body do accept POST
  - **B**: Validate no login → novo endpoint `/auth/password-verify` com email+password

**2. Endpoints de Auth com Password**
- `POST /auth/password-verify` (email, password, temp_password_token?) → sessão com force_password_reset=true
- `POST /auth/password-change` (old_password, new_password) → limpa force_password_reset
- `POST /auth/password-reset-request` (email) → envia token reset
- `POST /auth/password-reset` (token, new_password) → atualiza password_hash

**3. Middleware de Forçar Troca de Senha**
- Interceptar criação de sessão/verificação no `authn`
- Se `user.password_expires_at IS NOT NULL`: settar `session.force_password_reset=true`
- Middleware retorna 403 se acessar route que não seja `/settings/password` e flag está setada

**4. Frontend**
- Página `/settings/password` (ou modal ao login)
  - Se forçado: não deixa pular
  - Formulário: (old ou none se novo) + new + confirm
  - POST `/auth/password-change`
- Link "Esqueci a senha" na tela `/login`
  - Form email → POST `/auth/password-reset-request`
  - Recebe email com link `/reset-password/<token>`
  - Tela sem login: email + novo password
  - POST `/auth/password-reset`

**5. Consolidação de Agentes**
- ✅ IAM4.1 já implementado (agentes, perfil, filas, presença)
- ❌ UI em `/settings/agents` precisa estar completa
- ❌ Testes E2E do fluxo completo de onboarding

## Próximas Tasks (em Ordem)

### Task #2.5: Integrar password no fluxo de accept (1-2 horas)
- Modificar `AcceptInvitation()` em `invitations_http.go`
- Criar user (ou atualizar) com password_hash + password_expires_at
- Testes: `invitations_iam2c_test.go` precisa de novos casos

### Task #3: UI de Troca Obrigatória (1-2 horas)
- Frontend: página `/settings/password` ou modal
- Interceptar força no login (como?)
- Botão "OK" só ativa quando senhas batem

### Task #4: Endpoints de Reset (2-3 horas)
- `/auth/password-reset-request` + email template
- `/auth/password-reset` + validação de token
- Table `password_resets` + cleanup de tokens antigos
- Testes de expiração, token_hash segurança

### Task #5: Consolidar Agentes (1-2 horas)
- Verificar `/settings/agents` frontend
- API de CRUD de agentes (já existe?)
- Testes E2E: criar, pausar, reativar agente

### Task #6: Testes Integração (2-3 horas)
- E2E Playwright: convite → email → aceita → login → força troca → novo login
- Testes backend: migrations, password crypto, token expiry
- Mock SMTP ou Mailpit para dev

### Task #7: Deploy (1 hora)
- Migrations (up/down testadas)
- Backup do banco
- Docker compose
- Verificação ao vivo

## Decisões Abertas

1. **Aceitar convite com ou sem password?**
   - Opção A: `/invitations/<token>/accept` POST com `password` no body
   - Opção B: Aceitar sem password, validar no `/auth/password-verify` (mais riscos)
   - **Recomendação**: A (mais seguro)

2. **Frontend já tem interceptação de login forçado?**
   - Verificar se há middleware de authn no `web/src`
   - Pode ser flag na sessão ou estado do Redux/Context

3. **Agentes já têm UI completa?**
   - Verificar `/settings/agents` (handoff menciona "já existe")
   - Se não, precisa fazer

## Referências

- `docs/delivery/HANDOFF-NEXT-AGENT.md` – Estado do IAM4.1/4.2
- `docs/adr/0018-identity-accounts.md` – ADR de identidade
- `internal/platform/authn/` – OIDC/sessões
- `migrations/000037_invitation_delivery_email_verified.sql` – Base de convites
- `internal/password/password.go` – Crypto nova (este documento)

---

**Próximo passo**: Task #2.5 — modificar `AcceptInvitation()` para guardar password_hash e testes.
