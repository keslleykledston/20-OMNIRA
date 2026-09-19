-- D3.1: Create channel_credentials table (tenant-owned, RLS+FORCE)
-- Stores encrypted channel provider credentials (access tokens, session keys, etc.)

CREATE TABLE IF NOT EXISTS channel_credentials (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  connection_id UUID NOT NULL,
  tenant_id UUID NOT NULL,
  nonce BYTEA NOT NULL,           -- 12 bytes, unique per encryption call
  ciphertext BYTEA NOT NULL,      -- AES-256-GCM output
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),

  -- Foreign key constraints
  CONSTRAINT fk_channel_credentials_connection
    FOREIGN KEY (connection_id)
    REFERENCES channel_connections(id) ON DELETE CASCADE,

  CONSTRAINT fk_channel_credentials_tenant
    FOREIGN KEY (tenant_id)
    REFERENCES tenants(id) ON DELETE CASCADE
);

-- Indexes for common queries
CREATE INDEX IF NOT EXISTS idx_channel_credentials_connection_id
  ON channel_credentials(connection_id);

CREATE INDEX IF NOT EXISTS idx_channel_credentials_tenant_id
  ON channel_credentials(tenant_id);

-- RLS Policy: tenant-owned, FORCE enabled
-- Each tenant can only access credentials associated with their tenant_id
ALTER TABLE channel_credentials ENABLE ROW LEVEL SECURITY;

DROP POLICY IF EXISTS channel_credentials_tenant_policy ON channel_credentials;

CREATE POLICY channel_credentials_tenant_policy ON channel_credentials
  FOR ALL
  USING (tenant_id = current_setting('app.tenant_id')::UUID)
  WITH CHECK (tenant_id = current_setting('app.tenant_id')::UUID);

-- FORCE: even superuser must respect the policy
ALTER TABLE channel_credentials FORCE ROW LEVEL SECURITY;

-- Ensure updated_at is refreshed automatically
CREATE OR REPLACE FUNCTION update_updated_at_column()
RETURNS TRIGGER AS $$
BEGIN
  NEW.updated_at = NOW();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE OR REPLACE TRIGGER channel_credentials_updated_at
  BEFORE UPDATE ON channel_credentials
  FOR EACH ROW
  EXECUTE FUNCTION update_updated_at_column();
