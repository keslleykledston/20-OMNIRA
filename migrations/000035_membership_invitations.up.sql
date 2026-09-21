-- Convites de uso único para ingressar num tenant com um papel definido.
-- O token bruto nunca é persistido — só o hash (sha256, ver
-- internal/tenancy/adapters/invitations_http.go) — porque um dump do banco
-- não pode virar uma lista de credenciais válidas.
CREATE TABLE membership_invitations (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  email TEXT NOT NULL,
  role_id UUID NOT NULL REFERENCES roles(id) ON DELETE RESTRICT,
  token_hash TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'accepted', 'revoked')),
  created_by UUID NOT NULL REFERENCES users(id) ON DELETE RESTRICT,
  accepted_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  accepted_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  expires_at TIMESTAMPTZ NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id)
);

-- expired não é um status próprio: é pending cujo expires_at passou. Guardar
-- como coluna geraria drift entre o valor armazenado e o relógio; a API
-- deriva o status efetivo na leitura.
CREATE UNIQUE INDEX idx_membership_invitations_token_hash ON membership_invitations(token_hash);
CREATE INDEX idx_membership_invitations_tenant_status ON membership_invitations(tenant_id, status, created_at DESC);

-- No máximo um convite pendente por (tenant, email): reenviar precisa revogar
-- o anterior primeiro, então nunca existem dois pendentes indistinguíveis.
CREATE UNIQUE INDEX idx_membership_invitations_pending_unique
  ON membership_invitations(tenant_id, lower(email))
  WHERE status = 'pending';

ALTER TABLE membership_invitations ENABLE ROW LEVEL SECURITY;
ALTER TABLE membership_invitations FORCE ROW LEVEL SECURITY;

-- SELECT também serve o fluxo de aceite: quem já se autenticou mas ainda não
-- tem membership no tenant do convite precisa conseguir ler o próprio
-- convite pelo hash antes de a membership existir. can_read_tenant_peer não
-- serve aqui (não há membership do lado do convidado ainda), então a policy
-- é: quem tem membership.read no tenant (fluxo de gestão), OU o convite é
-- para o e-mail da identidade autenticada (fluxo de aceite) — e-mail é
-- atributo, não é garantia de identidade, mas isso é o mesmo padrão da
-- resolução de OIDC no restante do produto.
CREATE OR REPLACE FUNCTION can_read_invitation(p_tenant_id UUID, p_email TEXT) RETURNS BOOLEAN AS $$
  SELECT EXISTS (
    SELECT 1 FROM memberships m
    JOIN role_permissions rp ON rp.role_id = m.role_id
    WHERE m.tenant_id = p_tenant_id AND m.user_id = current_user_id()
      AND m.status = 'active' AND rp.permission_key = 'membership.read'
  ) OR EXISTS (
    SELECT 1 FROM users u
    WHERE u.id = current_user_id() AND lower(u.email) = lower(p_email)
  );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = public;

REVOKE ALL ON FUNCTION can_read_invitation(UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION can_read_invitation(UUID, TEXT) TO omnira_app;

CREATE POLICY membership_invitations_read ON membership_invitations FOR SELECT
  USING (can_read_invitation(tenant_id, email) OR is_system_admin());

-- Criar e revogar exigem membership.manage — o mesmo permission que já
-- governa PATCH /team. Aceitar é um UPDATE (pending->accepted) feito pelo
-- próprio convidado: não tem membership.manage ainda, então a policy de
-- update também aceita a mesma condição de e-mail usada na leitura.
CREATE POLICY membership_invitations_insert ON membership_invitations FOR INSERT
  WITH CHECK (
    EXISTS (
      SELECT 1 FROM memberships m
      JOIN role_permissions rp ON rp.role_id = m.role_id
      WHERE m.tenant_id = tenant_id AND m.user_id = current_user_id()
        AND m.status = 'active' AND rp.permission_key = 'membership.manage'
    )
  );

CREATE POLICY membership_invitations_update ON membership_invitations FOR UPDATE
  USING (can_read_invitation(tenant_id, email) OR is_system_admin())
  WITH CHECK (can_read_invitation(tenant_id, email) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE ON membership_invitations TO omnira_app;
