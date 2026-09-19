-- U2: referência segura da sessão externa WAHA.
-- O nome da sessão nunca é autoridade; ownership continua em tenant_id + connection id.
ALTER TABLE channel_connections
  ADD COLUMN provider_session_ref TEXT NOT NULL DEFAULT '';

ALTER TABLE channel_connections
  DROP CONSTRAINT channel_connections_status_check,
  ADD CONSTRAINT channel_connections_status_check
    CHECK (status IN ('pending', 'active', 'degraded', 'disconnected', 'failed', 'revoked'));

CREATE UNIQUE INDEX idx_channel_connections_provider_session_ref
  ON channel_connections(provider, provider_session_ref)
  WHERE provider_session_ref <> '';
