-- Reverter suporte a co-atendimento.
-- DROP TABLE leva junto índices, constraints, policies e grants da tabela;
-- o ENUM é um objeto separado e precisa cair explicitamente.
DROP TABLE IF EXISTS conversation_participants CASCADE;
DROP TYPE IF EXISTS conversation_participant_role;
