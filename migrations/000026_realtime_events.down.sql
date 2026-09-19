-- Undo only what 000026 created.
DROP TRIGGER IF EXISTS conversations_realtime_trg ON conversations;
DROP TRIGGER IF EXISTS messages_realtime_trg ON messages;
DROP FUNCTION IF EXISTS conversations_realtime();
DROP FUNCTION IF EXISTS messages_realtime();
DROP FUNCTION IF EXISTS realtime_emit(UUID, TEXT, UUID, JSONB);
