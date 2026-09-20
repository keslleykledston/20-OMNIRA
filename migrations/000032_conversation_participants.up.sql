-- Suporte a co-atendimento e transferência de atendimentos
-- Permite múltiplos técnicos atenderem uma conversation simultaneamente
-- e transferir responsabilidade de forma auditável

CREATE TYPE conversation_participant_role AS ENUM ('ASSIGNEE', 'INVITED', 'CO_ATTENDEE');

CREATE TABLE conversation_participants (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  tenant_id UUID NOT NULL,
  conversation_id UUID NOT NULL,
  user_id UUID NOT NULL,
  role conversation_participant_role NOT NULL DEFAULT 'INVITED',
  joined_at TIMESTAMP WITH TIME ZONE,
  left_at TIMESTAMP WITH TIME ZONE,
  created_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
  updated_at TIMESTAMP WITH TIME ZONE NOT NULL DEFAULT now(),
  CONSTRAINT fk_conversation FOREIGN KEY (conversation_id) REFERENCES conversations(id),
  CONSTRAINT fk_user REFERENCES users(id),
  CONSTRAINT unique_participant UNIQUE (conversation_id, user_id),
  CONSTRAINT check_role_dates CHECK (
    (role != 'CO_ATTENDEE' AND joined_at IS NULL) OR
    (role = 'CO_ATTENDEE' AND joined_at IS NOT NULL)
  )
);

CREATE INDEX idx_conversation_participants_conversation ON conversation_participants(conversation_id);
CREATE INDEX idx_conversation_participants_user ON conversation_participants(user_id);
CREATE INDEX idx_conversation_participants_role ON conversation_participants(role);

-- Comentários para futura referência
COMMENT ON TABLE conversation_participants IS
  'Rastreia participantes de uma conversation. ASSIGNEE = responsável principal, INVITED = convite pendente, CO_ATTENDEE = atendendo simultaneamente.';

COMMENT ON COLUMN conversation_participants.role IS
  'ASSIGNEE: responsável principal (pode transferir ou chamar co-attendees). INVITED: convite pendente (pode aceitar/rejeitar). CO_ATTENDEE: co-atendendo (pode sair voluntariamente).';

COMMENT ON COLUMN conversation_participants.joined_at IS
  'Timestamp quando CO_ATTENDEE entrou no atendimento (NULL para INVITED/ASSIGNEE).';

COMMENT ON COLUMN conversation_participants.left_at IS
  'Timestamp quando participante saiu (NULL se ainda ativo).';
