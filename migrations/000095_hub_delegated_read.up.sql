-- ADR-0026: Hub delegated READ access to tenant-owned resources (first vertical slice).
--
-- Additive, SELECT-only, PERMISSIVE policies. The existing tenant policies (has_active_membership(...)
-- OR is_system_admin()) are not touched, so direct members behave exactly as before. A Hub agent gets
-- read access only where has_active_hub_access() holds (member of the hub, hub active, grant and
-- contract live, queue allowed). There is deliberately NO INSERT/UPDATE/DELETE policy for Hub access
-- here: writing through a Hub (replying, assigning) is a later slice with its own tests.
-- Tables that hold credentials/integration state (channel_credentials, ticket_external_*,
-- tenant_ai_integrations, ...) get NO Hub policy and stay invisible to a Hub agent.

CREATE POLICY tenants_read_hub_delegation ON tenants FOR SELECT
  USING (has_active_hub_access(current_user_id(), id, NULL, NULL, false));

CREATE POLICY conversations_read_hub_delegation ON conversations FOR SELECT
  USING (has_active_hub_access(current_user_id(), tenant_id, queue_id));

-- A message is readable through a Hub only if its conversation is: the EXISTS runs under the caller's RLS, so
-- the queue scope of conversations_read_hub_delegation decides. The first clause therefore checks only that the
-- grant and contract are live (scope off), otherwise a queue-restricted contract would hide even the messages
-- of the queues it allows. The tenants row is not queue-bound either, so it also skips the allowlist.
CREATE POLICY messages_read_hub_delegation ON messages FOR SELECT
  USING (
    has_active_hub_access(current_user_id(), tenant_id, NULL, NULL, false)
    AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = messages.conversation_id)
  );
