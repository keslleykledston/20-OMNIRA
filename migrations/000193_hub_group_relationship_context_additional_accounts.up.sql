-- ADR-0059 addendum 8 (follow-up): the Hub Organizer's "Cliente do CRM" folders/Kanban and the
-- "Vincular cliente" action (migrations 000190/000191) only ever reflected wa_groups.account_id, the
-- group's ONE primary client — a "Swap"/peering group now linked to a SECOND client via
-- wa_group_additional_accounts (migration 000192) stayed invisible in the Hub Organizer's other client's
-- folder. This extends the existing read-only bridge with the same additional-accounts list the tenant's
-- own Groups page already shows (GroupAccountLink.tsx), as two parallel arrays (ids/names) since SQL has
-- no composite-array literal as convenient as Go's []AccountRef — the Go layer (service.go) zips them
-- back into one slice. Still never written here: adding/removing an additional account stays the
-- tenant's OWN POST/DELETE .../groups/{id}/accounts endpoint, called directly from the Hub Organizer
-- exactly like the primary link already is (ADR-0059 addendum 7).
--
-- Postgres does not allow CREATE OR REPLACE to change a function's return columns, so the prior version
-- (000191) is dropped first. Same body/authorization otherwise.
DROP FUNCTION IF EXISTS hub_group_catalog_relationship_context(UUID, UUID);

CREATE FUNCTION hub_group_catalog_relationship_context(p_hub_id UUID, p_user_id UUID)
RETURNS TABLE(hub_group_id UUID, wa_group_id UUID, purpose TEXT, account_id UUID, account_display_name TEXT,
              additional_account_ids UUID[], additional_account_names TEXT[]) AS $$
  SELECT c.id, g.id, g.purpose, ca.id, ca.name,
         COALESCE(extra.ids, ARRAY[]::UUID[]), COALESCE(extra.names, ARRAY[]::TEXT[])
  FROM public.hub_group_catalog c
  JOIN public.wa_groups g
    ON g.tenant_id = c.source_tenant_id
   AND g.channel_connection_id = c.source_channel_connection_id
   AND g.provider_group_id = c.provider_group_id
  LEFT JOIN public.customer_accounts ca
    ON ca.tenant_id = g.tenant_id AND ca.id = g.account_id
  LEFT JOIN LATERAL (
    SELECT array_agg(eca.id ORDER BY eca.name) AS ids, array_agg(eca.name ORDER BY eca.name) AS names
    FROM public.wa_group_additional_accounts ea
    JOIN public.customer_accounts eca ON eca.tenant_id = ea.tenant_id AND eca.id = ea.account_id
    WHERE ea.tenant_id = g.tenant_id AND ea.group_id = g.id
  ) extra ON true
  WHERE c.hub_id = p_hub_id
    AND p_user_id = public.current_user_id()
    AND public.has_hub_manage_access(c.source_tenant_id, p_user_id, 'groups', c.hub_id);
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_group_catalog_relationship_context(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_group_catalog_relationship_context(UUID, UUID) TO omnira_app;
