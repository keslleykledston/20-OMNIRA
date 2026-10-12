-- ADR-0040 §24 (cost at scale): the Hub's own reads — the list and the counters of hub_inbox_items, and the conversations those rows hang from — asked
-- has_active_hub_access(...) once PER ROW (a SECURITY DEFINER function that joins six tables and cannot be inlined), twice per inbox row: once in the
-- policy of hub_inbox_items and once for each of the conversations the policy's sub-select scans. Measured by scripts/bench-hub-policies.sh with 30 000
-- items visible to a Hub agent (papel omnira_app, FORCE RLS): the list took 5.6 s and the counter 8.1 s. Production has ~220 items, so this was a limit of the
-- design, not an incident; the fix is the one migrations 114 and 118 applied to the delegated context: ask the relationship ONCE per statement.
--
-- hub_reach() answers, for the CURRENT PERSON only, which (hub, company) pairs are live right now and what queues each pair's contract allows:
--   * live      : the same conditions as has_active_hub_access (grant live, contract live, hub/company active, account active, hub membership held, contract
--                 scope a JSON object);
--   * unrestricted : the contract's scope names no queue list (every queue allowed);
--   * queue_texts  : the queues the scope lists when it lists them (canonical text, compared with queue_id::text; a scope whose queue_ids is not an array
--                 allows nothing, exactly like the original).
-- Policies use it inside uncorrelated sub-selects (hashed once per statement). The meaning is unchanged: the new test compares them, row by row, with the
-- original function over every shape of state that changes the answer.
CREATE OR REPLACE FUNCTION hub_reach() RETURNS TABLE (hub_id UUID, tenant_id UUID, unrestricted BOOLEAN, queue_texts TEXT[]) AS $$
  WITH me AS (SELECT public.current_user_id() AS uid)
  SELECT g.hub_id, g.tenant_id,
         COALESCE(bool_or(c.service_scope -> 'queue_ids' IS NULL), false),
         COALESCE(array_agg(e.q) FILTER (WHERE e.q IS NOT NULL), '{}'::text[])
  FROM me
  JOIN public.effective_access_grants g ON g.user_id = me.uid
  JOIN public.hub_tenant_service_contracts c ON c.id = g.service_contract_id
  JOIN public.service_hubs h ON h.id = g.hub_id
  JOIN public.hub_memberships hm ON hm.hub_id = g.hub_id AND hm.user_id = g.user_id
  JOIN public.tenants t ON t.id = g.tenant_id
  LEFT JOIN LATERAL (
    SELECT x AS q FROM jsonb_array_elements_text(
      CASE WHEN jsonb_typeof(c.service_scope -> 'queue_ids') = 'array' THEN c.service_scope -> 'queue_ids' ELSE '[]'::jsonb END) x
  ) e ON true
  WHERE me.uid IS NOT NULL AND public.user_is_active(me.uid)
    AND t.status = 'active' AND h.status = 'active'
    AND g.status = 'active' AND g.valid_from <= now() AND (g.valid_until IS NULL OR g.valid_until > now())
    AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
    AND jsonb_typeof(c.service_scope) = 'object'
  GROUP BY g.hub_id, g.tenant_id;
$$ LANGUAGE SQL STABLE SECURITY DEFINER ROWS 20 SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION hub_reach() FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_reach() TO omnira_app;

-- conversations, outside the delegated context (the Hub's own tab): same visibility as has_active_hub_access(current_user_id(), tenant_id, queue_id) — any hub
-- of the person, the queue scope ON.
ALTER POLICY conversations_read_hub_delegation ON conversations
  USING ((SELECT acting_hub()) IS NULL
         AND (tenant_id IN (SELECT r.tenant_id FROM hub_reach() r WHERE r.unrestricted)
              OR (tenant_id, queue_id::text) IN (SELECT r.tenant_id, q FROM hub_reach() r, unnest(r.queue_texts) q)));

-- hub_inbox_items: the same as before — a live pair for THIS hub and company (queue scope OFF: the projection's queue can be stale) AND the parent
-- conversation currently readable (which goes through the policy above, by the conversation's REAL queue).
ALTER POLICY hub_inbox_read ON hub_inbox_items
  USING (is_system_admin()
         OR ((hub_id, tenant_id) IN (SELECT r.hub_id, r.tenant_id FROM hub_reach() r)
             AND EXISTS (SELECT 1 FROM conversations c WHERE c.tenant_id = hub_inbox_items.tenant_id AND c.id = hub_inbox_items.conversation_id)));
