-- D3.1: conexões de canal e credenciais cifradas.
-- O ciphertext nunca é retornado ao domínio fora do CredentialStore.

CREATE TABLE channel_connections (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  channel TEXT NOT NULL,
  provider TEXT NOT NULL,
  provider_kind TEXT NOT NULL CHECK (provider_kind IN ('official', 'unofficial')),
  external_account_id TEXT NOT NULL DEFAULT '',
  external_number_id TEXT NOT NULL,
  status TEXT NOT NULL DEFAULT 'pending'
    CHECK (status IN ('pending', 'active', 'degraded', 'disconnected', 'revoked')),
  capabilities JSONB NOT NULL DEFAULT '[]'::jsonb,
  secret_ref UUID,
  risk_acknowledged_at TIMESTAMPTZ,
  risk_acknowledged_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (provider, external_number_id)
);

CREATE INDEX idx_channel_connections_tenant_id ON channel_connections(tenant_id);
CREATE INDEX idx_channel_connections_external_number
  ON channel_connections(provider, external_number_id);

CREATE TABLE channel_credentials (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  connection_id UUID NOT NULL REFERENCES channel_connections(id) ON DELETE CASCADE,
  ciphertext BYTEA NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (connection_id)
);

ALTER TABLE channel_connections
  ADD CONSTRAINT channel_connections_secret_ref_fk
  FOREIGN KEY (secret_ref) REFERENCES channel_credentials(id) ON DELETE SET NULL;

ALTER TABLE channel_connections ENABLE ROW LEVEL SECURITY;
ALTER TABLE channel_credentials ENABLE ROW LEVEL SECURITY;

CREATE POLICY channel_connections_read_tenant ON channel_connections FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_connections_insert_tenant ON channel_connections FOR INSERT
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_connections_update_tenant ON channel_connections FOR UPDATE
  USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_connections_delete_tenant ON channel_connections FOR DELETE
  USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());

CREATE POLICY channel_credentials_read_tenant ON channel_credentials FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_credentials_write_tenant ON channel_credentials FOR INSERT
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_credentials_update_tenant ON channel_credentials FOR UPDATE
  USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY channel_credentials_delete_tenant ON channel_credentials FOR DELETE
  USING (has_active_admin_membership(tenant_id, current_user_id()) OR is_system_admin());

ALTER TABLE channel_connections FORCE ROW LEVEL SECURITY;
ALTER TABLE channel_credentials FORCE ROW LEVEL SECURITY;

GRANT SELECT, INSERT, UPDATE, DELETE ON channel_connections, channel_credentials TO omnira_app;
