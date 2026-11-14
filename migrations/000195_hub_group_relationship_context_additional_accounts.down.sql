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
