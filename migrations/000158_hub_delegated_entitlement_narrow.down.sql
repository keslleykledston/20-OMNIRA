SET LOCAL lock_timeout = '5s';
DROP POLICY IF EXISTS tenant_entitlements_read_delegated ON tenant_entitlements;
CREATE POLICY tenant_entitlements_read_delegated ON tenant_entitlements FOR SELECT
  USING (acting_hub() IS NOT NULL AND tenant_id = acting_tenant());
