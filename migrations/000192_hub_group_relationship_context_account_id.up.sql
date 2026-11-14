-- ADR-0059 addendum 6: a second, account-based grouping for the Hub Organizer's Pastas/Kanban views
-- (owner feedback, 2026-11-12) — a folder per CRM customer_account instead of per purpose tag, read-only
-- same as before. Grouping correctly across tenants needs the account's own id, not just its display
-- name (two different tenants could coincidentally link an account with the same name), so this adds
-- account_id to hub_group_catalog_relationship_context's result. account_id is an opaque reference, no
-- more sensitive than the display name already returned — still never exposes enabled, last_message, or
-- anything from wa_group_messages.
--
-- Postgres does not allow CREATE OR REPLACE to change a function's return columns, so the old 3-column
-- version is dropped first. Same body/authorization otherwise: has_hub_manage_access re-checked per row
-- against that row's own source_tenant_id/hub_id, p_user_id must equal the caller's own current_user_id().
DROP FUNCTION IF EXISTS hub_group_catalog_relationship_context(UUID, UUID);

CREATE FUNCTION hub_group_catalog_relationship_context(p_hub_id UUID, p_user_id UUID)
RETURNS TABLE(hub_group_id UUID, purpose TEXT, account_id UUID, account_display_name TEXT) AS $$
  SELECT c.id, g.purpose, ca.id, ca.name
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
