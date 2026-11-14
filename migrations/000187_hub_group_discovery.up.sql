-- ADR-0059 addendum: discovery bridge for the group organizer's "add to catalog" flow. The manual
-- catalog entry (Gate 1/2) left the operator typing provider_group_id/connection_id by hand with
-- nowhere in the product to copy them from — found live in production. This function lets a Hub
-- actor who already has 'groups' management access to a tenant see the REAL groups already known
-- to that tenant's wa_groups table (name + provider_group_id only), instead of guessing a JID.
--
-- Deliberately narrow:
--   * reads ONLY wa_groups.provider_group_id/name — never enabled (ADR-0015 ingestion boundary),
--     never wa_group_messages, never participant identities.
--   * sourced from wa_groups, not a live WAHA call: a group the tenant's own team has never opened
--     once in their Groups page (so it was never written to wa_groups) will not show up here yet.
--     Calling WAHA live would need 'channels'-scoped channel_credentials, a bigger grant than
--     'groups' was ever meant to carry (see ADR-0059 §3) — explicitly out of scope for this fix.
--   * fails closed to an EMPTY result, never an error, when the caller lacks 'groups' access to
--     p_tenant_id — this must never reveal whether the tenant or the connection exists.
--   * p_user_id must equal the caller's own current_user_id() (same rule has_hub_manage_access
--     itself enforces) — a caller cannot discover groups "as" someone else.
CREATE OR REPLACE FUNCTION hub_discover_tenant_groups(p_hub_id UUID, p_tenant_id UUID, p_channel_connection_id UUID, p_user_id UUID)
RETURNS TABLE(provider_group_id TEXT, name TEXT) AS $$
  SELECT g.provider_group_id, g.name
  FROM public.wa_groups g
  WHERE g.tenant_id = p_tenant_id
    AND g.channel_connection_id = p_channel_connection_id
    AND p_user_id = public.current_user_id()
    AND public.has_hub_manage_access(p_tenant_id, p_user_id, 'groups', p_hub_id)
  ORDER BY lower(g.name), g.provider_group_id;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_discover_tenant_groups(UUID, UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_discover_tenant_groups(UUID, UUID, UUID, UUID) TO omnira_app;
