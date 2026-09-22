DROP INDEX IF EXISTS idx_conversations_routing_retry;
ALTER TABLE conversations DROP COLUMN routing_retry_at;
