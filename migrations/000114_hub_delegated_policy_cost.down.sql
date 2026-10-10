CREATE OR REPLACE FUNCTION lock_served_tenant(p_tenant_id UUID, p_user_id UUID, p_hub_id UUID) RETURNS BOOLEAN AS $$
BEGIN
  IF NOT (p_user_id = public.current_user_id() OR public.is_system_admin()) THEN
    RETURN false;
  END IF;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM 1 FROM public.tenants WHERE id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.service_hubs WHERE id = p_hub_id FOR SHARE;
  PERFORM 1 FROM public.hub_tenant_service_contracts WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id FOR SHARE;
  PERFORM 1 FROM public.hub_memberships WHERE hub_id = p_hub_id AND user_id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.users WHERE id = p_user_id FOR SHARE;
  PERFORM 1 FROM public.effective_access_grants WHERE hub_id = p_hub_id AND tenant_id = p_tenant_id AND user_id = p_user_id FOR SHARE;
  IF cardinality(public.delegated_permissions(p_tenant_id, p_user_id, p_hub_id)) = 0 THEN
    RETURN false;
  END IF;
  PERFORM set_config('app.acting_hub', p_hub_id::TEXT, true);
  PERFORM set_config('app.acting_tenant', p_tenant_id::TEXT, true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
ALTER POLICY conversations_acting_one_instance ON conversations USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY contacts_acting_one_instance ON contacts USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY contact_account_links_acting_one_instance ON contact_account_links USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY customer_accounts_acting_one_instance ON customer_accounts USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY tickets_acting_one_instance ON tickets USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY ticket_external_create_attempts_acting_one_instance ON ticket_external_create_attempts USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY crm_contact_company_evidence_acting_one_instance ON crm_contact_company_evidence USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY account_external_links_acting_one_instance ON account_external_links USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY contact_notes_acting_one_instance ON contact_notes USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
ALTER POLICY contact_notes_read_delegated ON contact_notes
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
ALTER POLICY contact_notes_read_tenant ON contact_notes USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY message_media_read_tenant ON message_media USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY tickets_read_tenant ON tickets USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY contacts_read_tenant ON contacts USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY messages_read_tenant ON messages USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY conversations_read_tenant ON conversations USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
ALTER POLICY messages_acting_hub_only ON messages
  USING (public.acting_hub() IS NULL
         OR public.has_active_hub_access(public.current_user_id(), tenant_id, NULL, public.acting_hub(), false));
ALTER POLICY messages_read_hub_delegation ON messages
  USING (
    has_active_hub_access(current_user_id(), tenant_id, NULL, NULL, false)
    AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = messages.conversation_id)
  );
DROP POLICY IF EXISTS messages_read_acting ON messages;
ALTER POLICY conversations_acting_hub_only ON conversations
  USING (public.acting_hub() IS NULL
         OR public.has_active_hub_access(public.current_user_id(), tenant_id, queue_id, public.acting_hub(), true));
ALTER POLICY conversations_read_hub_delegation ON conversations
  USING (has_active_hub_access(current_user_id(), tenant_id, queue_id));
DROP POLICY IF EXISTS conversations_read_acting ON conversations;
DROP FUNCTION IF EXISTS acting_scope();
