-- ADR-0059 addendum 9 (owner, 2026-11-12): every catalog entry's own origin tenant should be able to see
-- it linked to ITSELF by default ("Vincular a um Tenant" is still a real, separate action for genuinely
-- sharing a group with ANOTHER tenant) — found live in production: 33 of 34 catalog entries of the
-- K3G_Grupos Hub had NO hub_group_tenant_links row at all, because that link was only ever created by an
-- explicit, one-at-a-time click, never automatically. Role 'operator' (decorative label only, see
-- validRoles in internal/hub/grouporganizer/service.go — nothing is enforced by it).
--
-- Two parts:
-- 1) one-time, idempotent backfill for every catalog entry across every hub/tenant that has no link to
--    its own origin tenant yet (never touches one that already has ANY link, active or revoked — a
--    deliberate prior revoke is never silently undone);
-- 2) hub_autodiscover_catalog_upsert (migration 000187) now also ensures that self-link going forward,
--    for every future group the background worker discovers. The OTHER creation path (the Hub operator's
--    own "Adicionar grupo ao catálogo", UpsertCatalogEntry in service.go) gets the equivalent logic in Go,
--    in the same transaction — see that function's diff.
INSERT INTO hub_group_tenant_links (hub_id, hub_group_id, tenant_id, role)
SELECT c.hub_id, c.id, c.source_tenant_id, 'operator'
FROM hub_group_catalog c
WHERE NOT EXISTS (
  SELECT 1 FROM hub_group_tenant_links l WHERE l.hub_group_id = c.id AND l.tenant_id = c.source_tenant_id
);

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
  ),
  self_link AS (
    INSERT INTO hub_group_tenant_links (hub_id, hub_group_id, tenant_id, role)
    SELECT p_hub_id, upserted.id, p_tenant_id, 'operator' FROM upserted
    WHERE NOT EXISTS (
      SELECT 1 FROM hub_group_tenant_links l WHERE l.hub_group_id = upserted.id AND l.tenant_id = p_tenant_id
    )
    RETURNING 1
  )
  SELECT upserted.id, upserted.created FROM upserted LEFT JOIN self_link ON true;
$$ LANGUAGE SQL VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;

REVOKE EXECUTE ON FUNCTION hub_autodiscover_catalog_upsert(UUID, UUID, UUID, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION hub_autodiscover_catalog_upsert(UUID, UUID, UUID, TEXT, TEXT) TO omnira_app;
