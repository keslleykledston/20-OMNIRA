DROP TRIGGER IF EXISTS wa_group_messages_persisted_event ON wa_group_messages;
DROP TRIGGER IF EXISTS messages_persisted_event ON messages;
DROP FUNCTION IF EXISTS inbox_message_persisted_event();
DROP TABLE IF EXISTS intelligence_jobs;
