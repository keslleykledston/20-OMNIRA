-- ADR-0040 phase 04b: a Hub agent may use the instance's ERP directory and open the ERP ticket of a conversation it holds.
--
-- What this opens, and how (the same shape as 000109/110: the data layer answers by DOMAIN, the route and the handler answer by KEY):
--   * `tickets` (read; update) and `ticket_external_create_attempts` (read, insert, update) by the `ticket` domain, each narrowed to conversations the caller
--     can see (so a contract limited to some queues never reaches the tickets of the others) and pinned to the instance the request acts for (000111).
--     A delegate gets NO insert on `tickets` (the local ticket of a conversation is made by the platform), no delete, and no access to the status-attempt table
--     (changing the ERP status is a later step).
--   * `crm_contact_company_evidence` (read; insert; update): the best-effort fact "this contact was tied to this company by an operator", made after a ticket is
--     opened and read when classifying. By the contact domain/ticket domain, narrowed to contacts the caller sees, pinned to the instance.
--   * The two things that must NOT be a table policy go through narrow SECURITY DEFINER functions that check the key themselves:
--       - delegated_erp_connections: the ERP connection(s) of the instance and the ENCRYPTED credential (never the clear text; the server decrypts it in memory with
--         its own key, exactly as it does for a member), only while acting for a hub in THIS instance and only for a person who holds ticket.create or
--         contact.classify right now. channel_credentials stays unreadable to a delegate.
--       - delegated_materialize_company_account: the local account (and its link) that represents a company the ERP directory just validated. A delegate may not
--         insert accounts or links directly; this function does exactly find-or-create-and-activate, for the key that matches the reason (ticket.create for the
--         ticket flow, contact.classify for the classification flow).

-- 1. tickets
CREATE POLICY tickets_read_delegated ON tickets FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = tickets.conversation_id AND c.tenant_id = tickets.tenant_id));
CREATE POLICY tickets_update_delegated ON tickets FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = tickets.conversation_id AND c.tenant_id = tickets.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write')));
CREATE POLICY tickets_acting_one_instance ON tickets AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());

-- 2. ticket_external_create_attempts
CREATE POLICY ticket_external_create_attempts_read_delegated ON ticket_external_create_attempts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id AND c.tenant_id = ticket_external_create_attempts.tenant_id));
CREATE POLICY ticket_external_create_attempts_insert_delegated ON ticket_external_create_attempts FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id AND c.tenant_id = ticket_external_create_attempts.tenant_id));
CREATE POLICY ticket_external_create_attempts_update_delegated ON ticket_external_create_attempts FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = ticket_external_create_attempts.conversation_id AND c.tenant_id = ticket_external_create_attempts.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write')));
CREATE POLICY ticket_external_create_attempts_acting_one_instance ON ticket_external_create_attempts AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());

-- 3. crm_contact_company_evidence
CREATE POLICY crm_contact_company_evidence_read_delegated ON crm_contact_company_evidence FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = crm_contact_company_evidence.contact_id AND ct.tenant_id = crm_contact_company_evidence.tenant_id));
CREATE POLICY crm_contact_company_evidence_insert_delegated ON crm_contact_company_evidence FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = crm_contact_company_evidence.contact_id AND ct.tenant_id = crm_contact_company_evidence.tenant_id));
CREATE POLICY crm_contact_company_evidence_update_delegated ON crm_contact_company_evidence FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'write'))
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = crm_contact_company_evidence.contact_id AND ct.tenant_id = crm_contact_company_evidence.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('ticket', 'write')));
CREATE POLICY crm_contact_company_evidence_acting_one_instance ON crm_contact_company_evidence AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());

-- 3b. account_external_links: READ only (which local account stands for which ERP company; the company-suggestions list joins it). Written only by the function in 5.
CREATE POLICY account_external_links_read_delegated ON account_external_links FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read')));

-- 4. The ERP connection(s) of the instance and the ENCRYPTED credential. Mirrors what the resolver reads for a member (every k3g_crm/erp connection of the
--    tenant, the credential row its secret_ref names); the server counts them, decrypts and builds the client. Nothing is returned for another instance.
CREATE FUNCTION delegated_erp_connections(p_tenant UUID) RETURNS TABLE (connection_id UUID, secret_ref UUID, ciphertext BYTEA) AS $$
BEGIN
  IF public.acting_hub() IS NULL OR public.acting_tenant() IS DISTINCT FROM p_tenant
     OR NOT (public.actor_has_permission(p_tenant, public.current_user_id(), 'ticket.create')
             OR public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')) THEN
    RAISE EXCEPTION 'delegated ERP access not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY
    SELECT c.id, c.secret_ref, cr.ciphertext
    FROM public.channel_connections c
    LEFT JOIN public.channel_credentials cr ON cr.id = c.secret_ref AND cr.tenant_id = c.tenant_id
    WHERE c.tenant_id = p_tenant AND c.provider = 'k3g_crm' AND c.channel = 'erp'
    ORDER BY c.created_at DESC;
END
$$ LANGUAGE plpgsql STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- 5. The local account of a company the ERP directory just validated (the Go handler validates it; this only records it). Find-or-create-and-activate, atomically.
CREATE FUNCTION delegated_materialize_company_account(p_tenant UUID, p_connection UUID, p_external_id TEXT, p_name TEXT, p_source TEXT) RETURNS UUID AS $$
DECLARE
  v_key TEXT;
  v_ext TEXT := btrim(p_external_id);
  v_name TEXT := left(btrim(COALESCE(p_name, '')), 200);
  v_account UUID;
  v_link public.account_external_links%ROWTYPE;
BEGIN
  v_key := CASE p_source WHEN 'ticket_flow' THEN 'ticket.create' WHEN 'directory_selection' THEN 'contact.classify' END;
  IF v_key IS NULL OR public.acting_hub() IS NULL OR public.acting_tenant() IS DISTINCT FROM p_tenant
     OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), v_key) THEN
    RAISE EXCEPTION 'delegated account materialization not permitted' USING ERRCODE = '42501';
  END IF;
  IF v_ext = '' OR char_length(v_ext) > 128
     OR NOT EXISTS (SELECT 1 FROM public.channel_connections c WHERE c.id = p_connection AND c.tenant_id = p_tenant AND c.provider = 'k3g_crm' AND c.channel = 'erp') THEN
    RAISE EXCEPTION 'invalid company reference' USING ERRCODE = '22023';
  END IF;
  IF v_name = '' THEN v_name := left('Empresa ' || v_ext, 200); END IF;

  SELECT * INTO v_link FROM public.account_external_links
   WHERE tenant_id = p_tenant AND provider = 'k3g' AND connection_id = p_connection AND external_company_id = v_ext;
  IF FOUND THEN
    IF v_link.status <> 'active' THEN
      UPDATE public.account_external_links SET status = 'active', updated_at = now() WHERE id = v_link.id;
    END IF;
    RETURN v_link.account_id;
  END IF;

  INSERT INTO public.customer_accounts (tenant_id, name, account_type) VALUES (p_tenant, v_name, 'customer') RETURNING id INTO v_account;
  INSERT INTO public.account_external_links (tenant_id, account_id, provider, connection_id, external_company_id, external_name_snapshot, source, verified_at)
  VALUES (p_tenant, v_account, 'k3g', p_connection, v_ext, v_name, p_source, now())
  ON CONFLICT (tenant_id, provider, connection_id, external_company_id) DO NOTHING;
  IF NOT FOUND THEN
    -- another request linked the company first: keep theirs, retire the orphan we just made
    UPDATE public.customer_accounts SET status = 'archived', archived_at = now(), updated_at = now() WHERE id = v_account AND tenant_id = p_tenant;
    SELECT * INTO v_link FROM public.account_external_links
     WHERE tenant_id = p_tenant AND provider = 'k3g' AND connection_id = p_connection AND external_company_id = v_ext;
    RETURN v_link.account_id;
  END IF;
  RETURN v_account;
END
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION delegated_erp_connections(UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION delegated_materialize_company_account(UUID, UUID, TEXT, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delegated_erp_connections(UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION delegated_materialize_company_account(UUID, UUID, TEXT, TEXT, TEXT) TO omnira_app;
