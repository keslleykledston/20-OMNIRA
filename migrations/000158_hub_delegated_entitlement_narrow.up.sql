-- Codex 2026-10-17 (MEDIUM) on 000157: the delegated read of tenant_entitlements showed EVERY switch of the instance. The only one a delegate's request needs is the
-- company's decision on "the Hub may change the ERP status" (the gate is asked in the delegate's own session). Narrow the policy to that capability; a future
-- delegated gate adds its own key here, on purpose.
SET LOCAL lock_timeout = '5s';
DROP POLICY IF EXISTS tenant_entitlements_read_delegated ON tenant_entitlements;
CREATE POLICY tenant_entitlements_read_delegated ON tenant_entitlements FOR SELECT
  USING (acting_hub() IS NOT NULL AND tenant_id = acting_tenant() AND capability = 'hub_ticket_status');
