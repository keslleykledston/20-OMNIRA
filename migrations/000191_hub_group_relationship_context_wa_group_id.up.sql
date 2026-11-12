-- ADR-0059 addendum 7: the Hub Organizer now offers an ACTION, not just a read projection — "Vincular ao
-- Cliente do CRM" directly from this page (owner feedback, 2026-11-12: the read-only version left no way
-- to actually index a group to a client). The action itself calls the ALREADY-EXISTING tenant-scoped
-- endpoint PUT /api/v1/tenants/{tenant_id}/groups/{group_id}/account (internal/groups/adapters/http.go,
-- ADR-0059 addendum 3) exactly as the tenant's own Groups page does — no new write path, no new
-- permission bridge: it authorizes a real tenant member (group.manage) the same way regardless of which
-- page the browser tab happens to be on. What was missing was only the group_id itself: the Hub
-- Organizer's hub_group_catalog id is a different id space than wa_groups.id, and the frontend has no
-- other way to learn the latter. This adds wa_groups.id (as wa_group_id) to the existing read-only
-- function so the frontend can address the group it already has unimpeachable, structurally-rechecked
-- read access to.
--
-- Postgres does not allow CREATE OR REPLACE to change a function's return columns, so the prior version
-- (000190) is dropped first. Same body/authorization otherwise.
DROP FUNCTION IF EXISTS hub_group_catalog_relationship_context(UUID, UUID);

CREATE FUNCTION hub_group_catalog_relationship_context(p_hub_id UUID, p_user_id UUID)
RETURNS TABLE(hub_group_id UUID, wa_group_id UUID, purpose TEXT, account_id UUID, account_display_name TEXT) AS $$
  SELECT c.id, g.id, g.purpose, ca.id, ca.name
  FROM public.hub_group_catalog c
  JOIN public.wa_groups g
    ON g.tenant_id = c.source_tenant_id
   AND g.channel_connection_id = c.source_channel_connection_id
   AND g.provider_group_id = c.provider_group_id
  LEFT JOIN public.customer_accounts ca
    ON ca.tenant_id = g.tenant_id AND ca.id = g.account_id
  WHERE c.hub_id = p_hub_id
    AND p_user_id = public.current_user_id()
    AND public.has_hub_manage_access(c.source_tenant_id, p_user_id, 'groups', c.hub_id);
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_group_catalog_relationship_context(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_group_catalog_relationship_context(UUID, UUID) TO omnira_app;
