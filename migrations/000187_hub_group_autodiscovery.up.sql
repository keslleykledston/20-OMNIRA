-- ADR-0059 addendum (autodiscovery): a new WhatsApp group should appear in the Hub's group organizer
-- catalog on its own, the moment a Hub<->tenant contract delegates 'groups' AND the tenant's own number
-- is a member of it -- without anyone first clicking "Ativar" (ADR-0015 stays untouched: the worker job
-- that calls this function only ever INSERTs wa_groups rows with enabled staying at its column default
-- of false, never setting enabled/enabled_by/enabled_at on an existing row -- that remains the ONE thing
-- the Enable handler in internal/groups/adapters/http.go does).
--
-- hub_group_catalog's RLS (migration 000183) has NO is_system_admin() bypass by design: a Hub member
-- must always prove 'groups' access through has_hub_manage_access(), and there is deliberately no
-- shortcut for "the session is a system admin". A background job has no human actor, so current_user_id()
-- is NULL inside its session and has_hub_manage_access() would always fail -- correctly, there is nobody
-- to authorize. This function is the narrow, explicit exception: SECURITY DEFINER, so it runs with the
-- function owner's privileges (bypassing hub_group_catalog's RLS, the same way hub_discover_tenant_groups
-- already does for wa_groups' RLS, migration 000185) and re-proves authorization STRUCTURALLY, inside its
-- own body, instead of trusting whatever triple the calling Go code already selected: a live
-- hub_tenant_service_contracts row for THIS (hub, tenant) with 'groups' in management_scopes, re-checked
-- at the moment of the call, not cached from an earlier read. There is no p_user_id parameter at all --
-- unlike hub_discover_tenant_groups/hub_discover_tenant_connections, which both require the caller's own
-- current_user_id(), this function must never accept an identity to impersonate, because none exists.
--
-- Fails closed to an EMPTY result (zero rows), never an error, when the contract is not live or lacks
-- 'groups' -- same convention as hub_discover_tenant_groups/hub_discover_tenant_connections (migrations
-- 000185/000186) -- so a contract revoked between the worker's own eligibility query and this call simply
-- writes nothing for that triple, which the worker treats as an expected race, not a failure.
--
-- Idempotent and non-destructive of human edits: ON CONFLICT updates ONLY last_seen_at/updated_at/version
-- -- it NEVER touches display_name or source_state of a row that already exists, so a Hub operator who
-- already renamed a catalog entry (or marked it archived) never has that overwritten by a later
-- autodiscovery pass. display_name is used only on the FIRST insert, as a reasonable starting value.
CREATE OR REPLACE FUNCTION hub_autodiscover_catalog_upsert(
  p_hub_id UUID, p_tenant_id UUID, p_channel_connection_id UUID, p_provider_group_id TEXT, p_display_name TEXT
) RETURNS TABLE(id UUID, created BOOLEAN) AS $$
  WITH authorized AS (
    SELECT 1
    FROM hub_tenant_service_contracts c
    JOIN service_hubs h ON h.id = c.hub_id
    WHERE c.hub_id = p_hub_id AND c.tenant_id = p_tenant_id
      AND c.status = 'active' AND c.valid_from <= now() AND (c.valid_until IS NULL OR c.valid_until > now())
      AND 'groups' = ANY(c.management_scopes)
      AND h.status = 'active'
  ),
  upserted AS (
    INSERT INTO hub_group_catalog (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id, display_name)
    SELECT p_hub_id, p_tenant_id, p_channel_connection_id, p_provider_group_id, p_display_name
    WHERE EXISTS (SELECT 1 FROM authorized)
    ON CONFLICT (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id)
    DO UPDATE SET last_seen_at = now(), updated_at = now(), version = hub_group_catalog.version + 1
    RETURNING hub_group_catalog.id AS id, (xmax = 0) AS created
  )
  SELECT * FROM upserted;
$$ LANGUAGE SQL VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_autodiscover_catalog_upsert(UUID, UUID, UUID, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_autodiscover_catalog_upsert(UUID, UUID, UUID, TEXT, TEXT) TO omnira_app;
