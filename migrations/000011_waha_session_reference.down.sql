DROP INDEX IF EXISTS idx_channel_connections_provider_session_ref;
UPDATE channel_connections SET status = 'degraded' WHERE status = 'failed';
ALTER TABLE channel_connections
  DROP CONSTRAINT channel_connections_status_check,
  ADD CONSTRAINT channel_connections_status_check
    CHECK (status IN ('pending', 'active', 'degraded', 'disconnected', 'revoked'));
ALTER TABLE channel_connections DROP COLUMN IF EXISTS provider_session_ref;
