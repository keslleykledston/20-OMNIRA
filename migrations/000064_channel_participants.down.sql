ALTER TABLE wa_group_messages
  DROP COLUMN IF EXISTS reply_to_external_message_id,
  DROP COLUMN IF EXISTS reply_to_group_message_id,
  DROP COLUMN IF EXISTS sender_channel_participant_id;
ALTER TABLE wa_group_messages DROP CONSTRAINT IF EXISTS wa_group_messages_tenant_id_uq;
DROP INDEX IF EXISTS messages_reply_to_idx;
ALTER TABLE messages
  DROP COLUMN IF EXISTS reply_to_external_message_id,
  DROP COLUMN IF EXISTS reply_to_message_id,
  DROP COLUMN IF EXISTS sender_channel_participant_id;
DROP TABLE IF EXISTS conversation_channel_participants;
DROP TABLE IF EXISTS channel_participants;
