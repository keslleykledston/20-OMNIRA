-- ADR-0040 phase 04d (cost): the delegated context read the inbox 20x slower than a member (measured, scripts/bench-hub-policies.sh: 30k conversations in the
-- instance, the list took 10.3 s against 0.46 s). Cause: every conversation and message row ran has_active_hub_access(...) — a SECURITY DEFINER function that
-- joins six tables and cannot be inlined — up to three times per row (the legacy permissive policy, the restrictive "this hub only" policy, and, for messages,
-- the conversation it hangs from). Nothing that function asks depends on the row EXCEPT the conversation's queue.
--
-- The fix keeps the meaning and moves the work: acting_scope() answers, ONCE per statement, everything the relationship says for the request's own person,
-- instance and hub (grant live, contract live, hub/company active, the contract's queue scope); the policies keep only the cheap per-row part (the instance
-- pin and "is my queue allowed"). It is used inside uncorrelated sub-selects, which PostgreSQL evaluates once as InitPlans.
--   * semantics are the SAME as has_active_hub_access(current_user_id(), tenant_id, queue_id, acting_hub(), true) for conversations and
--     (…, NULL, acting_hub(), false) for messages, row by row (proven by a test that compares them over the scope shapes);
--   * the legacy permissive policies (095) now apply only OUTSIDE the delegated context (acting_hub() IS NULL), where they behave exactly as before — the Hub's
--     own tab reads across instances through them; inside the delegated context the new permissive policies below grant the SAME visibility, for the one
--     instance of the context;
--   * the restrictive policies keep their role (the sum-of-hubs wall of 110) with the cheap predicate.

-- What the relationship says right now, for (current person, acting instance, acting hub). Always one row.
--   live_any     : a grant+contract pair is live (the conditions of has_active_hub_access with the queue scope OFF)
--   unrestricted : …and its contract scope names no queue list (every queue allowed)
--   queue_texts  : the queues the live pairs allow when the scope lists them (canonical text, compared with queue_id::text)
-- A contract scope that is not a JSON object, or whose queue_ids is not an array, allows nothing (same as the original function).
CREATE OR REPLACE FUNCTION acting_scope() RETURNS TABLE (live_any BOOLEAN, unrestricted BOOLEAN, queue_texts TEXT[]) AS $$
  WITH me AS (SELECT public.current_user_id() AS uid, public.acting_tenant() AS tenant, public.acting_hub() AS hub),
  pairs AS (
    SELECT c.service_scope AS sc
    FROM me
    JOIN public.effective_access_grants g ON g.user_id = me.uid AND g.tenant_id = me.tenant AND g.hub_id = me.hub
    JOIN public.hub_tenant_service_contracts c ON c.id = g.service_contract_id
    JOIN public.service_hubs h ON h.id = g.hub_id
    JOIN public.hub_memberships hm ON hm.hub_id = g.hub_id AND hm.user_id = g.user_id
    JOIN public.tenants t ON t.id = g.tenant_id
    WHERE me.uid IS NOT NULL AND public.user_is_active(me.uid)
      AND t.status = 'active' AND h.status = 'active'
      AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND jsonb_typeof(c.service_scope) = 'object'
  )
  SELECT COALESCE(count(*) > 0, false),
         COALESCE(bool_or(sc -> 'queue_ids' IS NULL), false),
         COALESCE((SELECT array_agg(e) FROM pairs p2, LATERAL jsonb_array_elements_text(p2.sc -> 'queue_ids') e
                   WHERE jsonb_typeof(p2.sc -> 'queue_ids') = 'array'), '{}'::text[])
  FROM pairs;
$$ LANGUAGE SQL STABLE SECURITY DEFINER ROWS 1 SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION acting_scope() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION acting_scope() TO omnira_app;

-- conversations: the delegated context sees the instance of the context, through the queues its contract allows.
CREATE POLICY conversations_read_acting ON conversations FOR SELECT
  USING ((SELECT acting_hub()) IS NOT NULL
         AND tenant_id = (SELECT acting_tenant())
         AND (SELECT s.live_any FROM acting_scope() s)
         AND ((SELECT s.unrestricted FROM acting_scope() s) OR queue_id::text = ANY (COALESCE((SELECT s.queue_texts FROM acting_scope() s), '{}'::text[]))));
ALTER POLICY conversations_read_hub_delegation ON conversations
  USING ((SELECT acting_hub()) IS NULL AND has_active_hub_access(current_user_id(), tenant_id, queue_id));
ALTER POLICY conversations_acting_hub_only ON conversations
  USING ((SELECT acting_hub()) IS NULL
         OR (tenant_id = (SELECT acting_tenant())
             AND (SELECT s.live_any FROM acting_scope() s)
             AND ((SELECT s.unrestricted FROM acting_scope() s) OR queue_id::text = ANY (COALESCE((SELECT s.queue_texts FROM acting_scope() s), '{}'::text[])))));

-- messages: readable when the grant and contract are live (queue scope off, like the original) and the conversation they hang from is visible.
CREATE POLICY messages_read_acting ON messages FOR SELECT
  USING ((SELECT acting_hub()) IS NOT NULL
         AND tenant_id = (SELECT acting_tenant())
         AND (SELECT s.live_any FROM acting_scope() s)
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = messages.conversation_id));
ALTER POLICY messages_read_hub_delegation ON messages
  USING ((SELECT acting_hub()) IS NULL
         AND has_active_hub_access(current_user_id(), tenant_id, NULL, NULL, false)
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = messages.conversation_id));
ALTER POLICY messages_acting_hub_only ON messages
  USING ((SELECT acting_hub()) IS NULL
         OR (tenant_id = (SELECT acting_tenant()) AND (SELECT s.live_any FROM acting_scope() s)));

-- The member's own read policies ask has_active_membership() for every row, and inside the delegated context that call is always false (it refuses while a
-- hub is the acting context, migration 109) but still costs a function call per row (~20 µs: a SECURITY DEFINER SQL function is planned and run each time).
-- Asking the same thing as an inline expression FIRST skips that call for a delegate and costs a members' request a single setting lookup. The answer never
-- changes: AND-ing a condition that has_active_membership() already contains cannot widen or narrow what it returns. Only the read policies of the six
-- tables the inbox scans are touched; insert/update/delete policies, and every other table, stay as they were.
ALTER POLICY conversations_read_tenant  ON conversations  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY messages_read_tenant       ON messages       USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY contacts_read_tenant       ON contacts       USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY tickets_read_tenant        ON tickets        USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY message_media_read_tenant  ON message_media  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());
ALTER POLICY contact_notes_read_tenant  ON contact_notes  USING ((NULLIF(current_setting('app.acting_hub', true), '') IS NULL AND has_active_membership(tenant_id, current_user_id())) OR is_system_admin());

-- The two lookups that say "which hub / which instance does this request act for" are SQL functions that PostgreSQL cannot inline (they pin their search_path),
-- so a policy that mentions them runs them once PER ROW — ~3 µs each, a dozen of them across the policies of one inbox scan, and the restrictive pins of
-- migrations 111-113 pay it even for members. Written as `(SELECT acting_hub())` the lookup is an uncorrelated sub-select, evaluated once per statement. The
-- pins keep their meaning exactly.
ALTER POLICY conversations_acting_one_instance                  ON conversations                  USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY contacts_acting_one_instance                       ON contacts                       USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY contact_account_links_acting_one_instance          ON contact_account_links          USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY customer_accounts_acting_one_instance              ON customer_accounts              USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY tickets_acting_one_instance                        ON tickets                        USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY ticket_external_create_attempts_acting_one_instance ON ticket_external_create_attempts USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY crm_contact_company_evidence_acting_one_instance   ON crm_contact_company_evidence   USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY account_external_links_acting_one_instance         ON account_external_links         USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
ALTER POLICY contact_notes_acting_one_instance                  ON contact_notes                  USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
-- the notes read: the key is asked ONCE for the instance of the context (the pin above makes every visible note belong to it)
ALTER POLICY contact_notes_read_delegated ON contact_notes
  USING ((SELECT acting_hub()) IS NOT NULL
         AND tenant_id = (SELECT acting_tenant())
         AND (SELECT actor_has_permission(acting_tenant(), current_user_id(), 'contact.note'))
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));

-- The delegated transaction does not JIT-compile (see below). Everything else in lock_served_tenant is unchanged from migration 111.
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
  -- the policies of the delegated context are costly on paper (nested function calls), which makes PostgreSQL's cost model switch JIT compilation on for an inbox
  -- scan; compiling ~250 functions took 1.2 s per statement, longer than the query itself (measured). Off for this transaction only.
  PERFORM set_config('jit', 'off', true);
  RETURN true;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
