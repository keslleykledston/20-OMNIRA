DROP TRIGGER IF EXISTS message_media_analysis_realtime_trg ON message_media_analysis;
DROP FUNCTION IF EXISTS message_media_analysis_realtime();
DROP TRIGGER IF EXISTS message_media_analysis_enqueue_trg ON message_media;
DROP FUNCTION IF EXISTS message_media_analysis_enqueue();
DROP TABLE IF EXISTS message_media_analysis;
