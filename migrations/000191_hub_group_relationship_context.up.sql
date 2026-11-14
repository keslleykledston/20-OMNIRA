-- ADR-0059 addendum 5: the Hub Organizer's "Vincular a um Tenant" modal does not answer what the
-- operator actually needs when (as for K3G_Grupos today) every group already belongs to the same
-- single tenant -- which CRM customer/partner/supplier this WhatsApp group is about, or whether it is
-- purely internal (feedback from the owner, 2026-11-12). That classification already exists on the
-- TENANT side: wa_groups.purpose (migration 000178, extended by 000190 with 'partner'/'supplier') plus
-- the optional wa_groups.account_id -> customer_accounts link (migration 000179, ADR-0059 addendum 3).
-- The write action stays exactly where it already is (the tenant's own Groups page, by someone with
-- group.manage membership there) -- this migration only lets the Hub Organizer SHOW it, read-only.
--
-- Same shape and same reasoning as hub_discover_tenant_groups (migration 000187): wa_groups/
-- customer_accounts have their own RLS that a Hub actor's session does not satisfy (no
-- has_active_membership on the origin tenant), so a plain join from the Hub's own session returns zero
-- rows. SECURITY DEFINER bypasses that RLS on purpose and re-proves authorization STRUCTURALLY inside
-- the function body instead of trusting the caller: has_hub_manage_access(..., 'groups', ...) is
-- re-checked per catalog row against that row's OWN source_tenant_id/hub_id, and p_user_id must equal
-- the caller's own current_user_id() (same rule has_hub_manage_access itself enforces) -- a caller
-- cannot ask "as" someone else.
--
-- Deliberately narrow: returns only purpose and the linked account's display NAME (never account_id,
-- never wa_groups.enabled, never last_message, never anything from wa_group_messages). One call for the
-- whole hub (not one per catalog entry) -- same N+1 lesson already learned this session for
-- hub_group_tenant_links (see ListLinksForHub).
CREATE OR REPLACE FUNCTION hub_group_catalog_relationship_context(p_hub_id UUID, p_user_id UUID)
RETURNS TABLE(hub_group_id UUID, purpose TEXT, account_display_name TEXT) AS $$
  SELECT c.id, g.purpose, ca.name
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
