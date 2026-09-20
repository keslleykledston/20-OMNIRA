-- Reverter suporte a co-atendimento
DROP INDEX idx_conversation_participants_role;
DROP INDEX idx_conversation_participants_user;
DROP INDEX idx_conversation_participants_conversation;
DROP TABLE conversation_participants;
DROP TYPE conversation_participant_role;
