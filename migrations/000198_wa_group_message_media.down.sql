DROP TRIGGER IF EXISTS wa_group_messages_media_enqueue ON wa_group_messages;
DROP FUNCTION IF EXISTS wa_group_message_media_enqueue();
DROP TABLE IF EXISTS wa_group_message_media;

ALTER TABLE wa_group_messages
  DROP CONSTRAINT IF EXISTS wa_group_messages_mime_type_len,
  DROP CONSTRAINT IF EXISTS wa_group_messages_media_ref_len;

ALTER TABLE wa_group_messages
  DROP COLUMN IF EXISTS mime_type,
  DROP COLUMN IF EXISTS media_ref;
