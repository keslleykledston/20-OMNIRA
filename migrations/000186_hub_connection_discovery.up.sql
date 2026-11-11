-- ADR-0059 addendum (second half): the group discovery bridge (migration 000185) still left the
-- operator typing a connection_id UUID by hand into "ID da conexão (canal) na origem" before the group
-- dropdown could even fire — found live in production right after 000185 shipped. This function lets a
-- Hub actor who already has 'groups' management access to a tenant see that tenant's WhatsApp channel
-- connections (id + a safe label), instead of guessing a UUID, so the UI can offer a connection picker
-- one step before the group picker.
--
-- Deliberately narrow, same shape as hub_discover_tenant_groups:
--   * reads ONLY channel_connections.id/channel/provider/external_number_id/status — never secret_ref,
--     never anything from channel_credentials (ciphertext never leaves that table's own boundary).
--   * filtered to channel = 'whatsapp': the group organizer has no use for ERP/other connections, and
--     'groups' was never meant to carry visibility into every channel a tenant has configured.
--   * fails closed to an EMPTY result, never an error, when the caller lacks 'groups' access to
--     p_tenant_id — this must never reveal whether the tenant or any connection exists.
--   * p_user_id must equal the caller's own current_user_id(), same rule has_hub_manage_access itself
--     enforces — a caller cannot discover connections "as" someone else.
CREATE OR REPLACE FUNCTION hub_discover_tenant_connections(p_hub_id UUID, p_tenant_id UUID, p_user_id UUID)
RETURNS TABLE(id UUID, channel TEXT, provider TEXT, external_number_id TEXT, status TEXT) AS $$
  SELECT c.id, c.channel, c.provider, c.external_number_id, c.status
  FROM public.channel_connections c
  WHERE c.tenant_id = p_tenant_id
    AND c.channel = 'whatsapp'
    AND p_user_id = public.current_user_id()
    AND public.has_hub_manage_access(p_tenant_id, p_user_id, 'groups', p_hub_id)
  ORDER BY c.status = 'active' DESC, c.external_number_id;
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_discover_tenant_connections(UUID, UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_discover_tenant_connections(UUID, UUID, UUID) TO omnira_app;
