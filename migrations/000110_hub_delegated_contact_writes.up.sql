-- ADR-0040 phase 04a: a Hub agent may CLASSIFY and EDIT the contact of an instance it serves (key contact.classify, domain `contact`, need write).
--
-- Same shape as 000109: the data layer answers by DOMAIN (the contact domain, write), the route and the handler answer by KEY (contact.classify,
-- asked live of actor_has_permission), and every policy inherits the visibility of the conversation the contact belongs to (the EXISTS runs under
-- the caller's RLS), so a contract limited to some queues never reaches the contacts of the others. Nothing else is opened: no INSERT or DELETE
-- of contacts, no notes (no key in the catalog yet), no creation or edit of customer accounts (the directory/ERP path is phase 04b).
--
-- What reclassifying a contact does besides updating the contact (and why there are two definer functions below):
--   * it re-derives conversation_kind on the contact's conversations and on the groups it spoke in (recompute_* functions, invoker rights);
--   * marking spam takes the contact's open, UNASSIGNED conversations out of their queue.
-- Both write `conversations`/`wa_groups`, which a delegate can only READ (conversations are changed by claiming/replying through the Hub's own
-- service). Opening a conversation UPDATE policy to the delegate would let them rewrite any column of the conversation, so those two effects go
-- through narrow SECURITY DEFINER functions that check the key themselves and do exactly that and nothing else.

-- 0. ONE hub at a time (Codex review of phase 04a, HIGH-1). The conversation and message policies of 000095 let ANY live grant of the person read a
--    conversation, whichever hub it came through; a person served by two hubs on the same instance, with different queue scopes, would therefore see
--    (and, through the contact and file policies that inherit that visibility, reach) the other hub's queues while acting for this one. A RESTRICTIVE
--    policy (it is AND-ed with every permissive one) closes it for good: while acting for a hub, a conversation is visible only through THAT hub's own
--    contract and grant, queue scope included, and a message only through that hub's live grant (its queue scope comes from the conversation, below).
--    Outside the delegated context (acting_hub() IS NULL) nothing changes: members and the Hub's own inbox behave as before.
CREATE POLICY conversations_acting_hub_only ON conversations AS RESTRICTIVE FOR SELECT
  USING (public.acting_hub() IS NULL
         OR public.has_active_hub_access(public.current_user_id(), tenant_id, queue_id, public.acting_hub(), true));
CREATE POLICY messages_acting_hub_only ON messages AS RESTRICTIVE FOR SELECT
  USING (public.acting_hub() IS NULL
         OR public.has_active_hub_access(public.current_user_id(), tenant_id, NULL, public.acting_hub(), false));

-- 1. contacts: UPDATE only.
CREATE POLICY contacts_update_delegated ON contacts FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'write'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.contact_id = contacts.id AND c.tenant_id = contacts.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('contact', 'write')));

-- 2. contact_account_links: read, add and end/promote (there is no DELETE policy for anyone: a link is ended, never erased).
CREATE POLICY contact_account_links_read_delegated ON contact_account_links FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read'))
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_account_links.contact_id AND ct.tenant_id = contact_account_links.tenant_id));
CREATE POLICY contact_account_links_insert_delegated ON contact_account_links FOR INSERT
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('contact', 'write'))
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_account_links.contact_id AND ct.tenant_id = contact_account_links.tenant_id));
CREATE POLICY contact_account_links_update_delegated ON contact_account_links FOR UPDATE
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'write'))
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_account_links.contact_id AND ct.tenant_id = contact_account_links.tenant_id))
  WITH CHECK (tenant_id IN (SELECT delegated_tenants('contact', 'write')));

-- 3. customer_accounts: READ only (to name and pick an existing account). The account directory of an instance is the company's own customer
--    list, not conversation data, so it is not narrowed by queue scope; the ROUTES that expose it ask for `account.read`.
CREATE POLICY customer_accounts_read_delegated ON customer_accounts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('contact', 'read')));

-- 4. The two effects of a reclassification that touch conversations/groups. Both refuse unless the request acts for a hub, the person holds
--    contact.classify in that instance RIGHT NOW (actor_has_permission) AND the contact has a conversation inside THIS hub's contract scope (so they cannot
--    be pointed at a contact the delegate cannot see). Their effect is the contact-level consequence of the classification itself: the derived kind of
--    every conversation and group of that contact is recomputed, and a spam contact's open, unassigned conversations leave their queues, exactly as
--    when a member does it (a derived value that stayed stale on the conversations outside the scope would be wrong, and a spammer left routable
--    in another queue would defeat the classification). Nothing is read or returned from outside the scope.
CREATE FUNCTION delegated_recompute_contact_kinds(p_tenant UUID, p_contact UUID) RETURNS TABLE (conversations INT, groups INT) AS $$
BEGIN
  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  RETURN QUERY SELECT public.recompute_contact_conversation_kinds(p_tenant, p_contact), public.recompute_groups_for_contact(p_tenant, p_contact);
END
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

-- Only for a contact that IS spam (so it cannot be used to empty the queue of anybody else) and only the open conversations nobody holds.
CREATE FUNCTION delegated_dequeue_spam(p_tenant UUID, p_contact UUID) RETURNS BIGINT AS $$
DECLARE n BIGINT;
BEGIN
  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE public.conversations c SET queue_id = NULL, routing_retry_at = NULL, updated_at = now()
  WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact AND c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM public.contacts ct WHERE ct.tenant_id = p_tenant AND ct.id = p_contact AND ct.kind = 'spam');
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION delegated_recompute_contact_kinds(UUID, UUID) FROM PUBLIC;
REVOKE EXECUTE ON FUNCTION delegated_dequeue_spam(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION delegated_recompute_contact_kinds(UUID, UUID) TO omnira_app;
GRANT EXECUTE ON FUNCTION delegated_dequeue_spam(UUID, UUID) TO omnira_app;
