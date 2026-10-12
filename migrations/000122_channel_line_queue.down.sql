DROP INDEX IF EXISTS idx_channel_connections_default_queue;
ALTER TABLE channel_connections DROP CONSTRAINT IF EXISTS channel_connections_default_queue_fk;
ALTER TABLE channel_connections DROP COLUMN IF EXISTS default_queue_id;
