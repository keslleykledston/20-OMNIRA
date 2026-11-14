-- Reverts hub_autodiscover_catalog_upsert to its pre-000194 body (no self-link side effect). Does NOT
-- delete the hub_group_tenant_links rows this migration backfilled or the function since created: a down
-- migration is a last-resort rollback, not a data-destructive undo, and those rows are real information
-- (a tenant linked to its own group) that may already be relied on by the time anyone rolls back.
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
