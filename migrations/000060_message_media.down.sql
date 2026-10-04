DROP TRIGGER IF EXISTS message_media_realtime_trg ON message_media;
DROP FUNCTION IF EXISTS message_media_realtime();
DROP TRIGGER IF EXISTS messages_media_enqueue ON messages;
DROP FUNCTION IF EXISTS message_media_enqueue();
DROP TABLE IF EXISTS message_media;
