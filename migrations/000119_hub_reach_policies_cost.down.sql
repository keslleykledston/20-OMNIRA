ALTER POLICY hub_inbox_read ON hub_inbox_items
  USING (is_system_admin()
         OR (has_active_hub_access(current_user_id(), tenant_id, NULL, hub_id, false)
             AND EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = hub_inbox_items.tenant_id AND c.id = hub_inbox_items.conversation_id)));
ALTER POLICY conversations_read_hub_delegation ON conversations
  USING ((SELECT acting_hub()) IS NULL AND has_active_hub_access(current_user_id(), tenant_id, queue_id));
DROP FUNCTION IF EXISTS hub_reach();
