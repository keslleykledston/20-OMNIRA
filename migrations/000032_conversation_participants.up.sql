-- Suporte a co-atendimento e transferência de atendimentos
-- Permite múltiplos técnicos atenderem uma conversation simultaneamente
-- e transferir responsabilidade de forma auditável

CREATE TYPE conversation_participant_role AS ENUM ('ASSIGNEE', 'INVITED', 'CO_ATTENDEE');

CREATE TABLE conversation_participants (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role conversation_participant_role NOT NULL DEFAULT 'INVITED',
  joined_at TIMESTAMPTZ,
  left_at TIMESTAMPTZ,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CONSTRAINT unique_participant UNIQUE (conversation_id, user_id),
  CONSTRAINT check_role_dates CHECK (
    (role != 'CO_ATTENDEE' AND joined_at IS NULL) OR
    (role = 'CO_ATTENDEE' AND joined_at IS NOT NULL)
  ),
  -- Composite FK, como tickets: impede persistir um participante cujo
  -- tenant_id diverge do tenant dono da conversation.
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX idx_conversation_participants_conversation ON conversation_participants(tenant_id, conversation_id);
CREATE INDEX idx_conversation_participants_user ON conversation_participants(tenant_id, user_id);

ALTER TABLE conversation_participants ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_participants FORCE ROW LEVEL SECURITY;

-- O repository lê, insere e atualiza (leave/accept são soft-update via
-- left_at/joined_at); nunca apaga linhas, então não há policy de DELETE.
CREATE POLICY conversation_participants_read_tenant ON conversation_participants FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY conversation_participants_insert_tenant ON conversation_participants FOR INSERT
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY conversation_participants_update_tenant ON conversation_participants FOR UPDATE
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

-- 000006 concede SELECT/INSERT/UPDATE/DELETE a toda tabela nova via ALTER
-- DEFAULT PRIVILEGES; o REVOKE tira o DELETE que esta tabela não usa, para que
-- o privilégio mínimo valha de fato e não apenas pela ausência de policy.
GRANT SELECT, INSERT, UPDATE ON conversation_participants TO omnira_app;
REVOKE DELETE ON conversation_participants FROM omnira_app;

-- Comentários para futura referência
COMMENT ON TABLE conversation_participants IS
  'Rastreia participantes de uma conversation. ASSIGNEE = responsável principal, INVITED = convite pendente, CO_ATTENDEE = atendendo simultaneamente.';

COMMENT ON COLUMN conversation_participants.role IS
  'ASSIGNEE: responsável principal (pode transferir ou chamar co-attendees). INVITED: convite pendente (pode aceitar/rejeitar). CO_ATTENDEE: co-atendendo (pode sair voluntariamente).';

COMMENT ON COLUMN conversation_participants.joined_at IS
  'Timestamp quando CO_ATTENDEE entrou no atendimento (NULL para INVITED/ASSIGNEE).';

COMMENT ON COLUMN conversation_participants.left_at IS
  'Timestamp quando participante saiu (NULL se ainda ativo).';
