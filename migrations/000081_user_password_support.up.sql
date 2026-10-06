-- IAM5: Suporte a password local para primeira autenticação pós-convite e reset de senha.
-- Keycloak continua como OIDC para login normal; password é apenas para:
-- 1. Primeira autenticação após receber convite (senha temporária)
-- 2. Reset de senha ("Esqueci a senha")
-- 3. Troca obrigatória de senha no primeiro login

ALTER TABLE users ADD COLUMN password_hash TEXT;

-- NULL = password ainda não foi definida (pós-convite, aguardando troca obrigatória).
-- Preenchido quando o usuário define sua primeira senha.
ALTER TABLE users ADD COLUMN password_set_at TIMESTAMPTZ;

-- NULL = password nunca expira. Se NOT NULL, força troca antes de usar a conta.
-- Setado para 72h no futuro quando um convite é criado (primeira autenticação).
ALTER TABLE users ADD COLUMN password_expires_at TIMESTAMPTZ;

-- Guarda o hash bcrypt da senha temporária gerada no convite, para validação no accept.
-- Nunca exposto em response; descartado após aceitar ou revogar o convite.
ALTER TABLE membership_invitations ADD COLUMN temporary_password_hash TEXT;

-- Auditoria: registra quando a senha foi resetada e por qual motivo.
CREATE TABLE password_resets (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  used_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);

CREATE INDEX idx_password_resets_token_hash ON password_resets(token_hash);
CREATE INDEX idx_password_resets_user_id ON password_resets(user_id, created_at DESC);
