-- A Hub agent may change the ERP status of the ticket of a conversation it holds (owner decision 2026-10-16: allowed, switchable per company, on by default).
--
-- Same shape as 000112 (the data layer answers by DOMAIN, the route and the handler answer by KEY ticket.update):
--   * ticket_external_status_attempts: read by the `ticket` read domain, insert and update by the `ticket` write domain, each narrowed to a conversation the caller can
--     see (so a contract limited to some queues never reaches the others) and pinned to the instance the request acts for. No delete for a delegate.
--   * tenant_entitlements: a delegate reads the switches of the ONE instance it acts for (the company's decision "the Hub may change the status" is asked in the
--     delegate's own session). Nothing else about the table opens: writes stay system-admin only.
SET LOCAL lock_timeout = '5s';

CREATE POLICY ticket_external_status_attempts_read_delegated ON ticket_external_status_attempts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_status_attempts.conversation_id AND c.tenant_id = ticket_external_status_attempts.tenant_id));
CREATE POLICY ticket_external_status_attempts_insert_delegated ON ticket_external_status_attempts FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_status_attempts.conversation_id AND c.tenant_id = ticket_external_status_attempts.tenant_id));
CREATE POLICY ticket_external_status_attempts_update_delegated ON ticket_external_status_attempts FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_status_attempts.conversation_id AND c.tenant_id = ticket_external_status_attempts.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_status_attempts.conversation_id AND c.tenant_id = ticket_external_status_attempts.tenant_id));
CREATE POLICY ticket_external_status_attempts_acting_one_instance ON ticket_external_status_attempts AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());

CREATE POLICY tenant_entitlements_read_delegated ON tenant_entitlements FOR SELECT
  USING (acting_hub() IS NOT NULL AND tenant_id = acting_tenant());
