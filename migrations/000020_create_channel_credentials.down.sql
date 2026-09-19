-- Rollback 000020: undo only what 000020.up created.
-- channel_credentials (table, unique/FK constraints, RLS, FORCE) belongs to 000010
-- and MUST NOT be dropped here.
DROP TRIGGER IF EXISTS channel_credentials_updated_at ON channel_credentials;
DROP FUNCTION IF EXISTS update_updated_at_column();
DROP POLICY IF EXISTS channel_credentials_tenant_policy ON channel_credentials;
DROP INDEX IF EXISTS idx_channel_credentials_tenant_id;
DROP INDEX IF EXISTS idx_channel_credentials_connection_id;
