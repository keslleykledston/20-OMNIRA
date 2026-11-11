-- ADR-0059 Gate 2: idempotency receipts for the group organizer's writes (link.create, link.transfer,
-- relationship.create). Same shape as service_command_receipts (migration 000150) and
-- company_creation_requests (000100): one row per (actor, scope, key), keeping the request's hash so a
-- retry with the SAME key and SAME body replays the first outcome, and the same key with a DIFFERENT
-- body is refused (never silently acts twice, never silently does something else).
--
-- Keyed by actor_id, not tenant_id: these writes can name two tenants (link destination, or both sides
-- of a relationship) and there is no single tenant to scope the receipt to. Mirrors company_creation_requests
-- (keyed by operator_id) for the same reason.
CREATE TABLE hub_group_organizer_receipts (
  hub_id                      UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  actor_id                    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  scope                       TEXT NOT NULL CHECK (scope IN ('link.create', 'link.transfer', 'relationship.create')),
  idempotency_key             TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash                TEXT NOT NULL,
  hub_group_tenant_link_id    UUID REFERENCES hub_group_tenant_links(id) ON DELETE CASCADE,
  hub_tenant_relationship_id  UUID REFERENCES hub_tenant_relationships(id) ON DELETE CASCADE,
  created_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),

  PRIMARY KEY (hub_id, actor_id, scope, idempotency_key),
  CONSTRAINT hub_group_organizer_receipts_target CHECK (
    (scope IN ('link.create', 'link.transfer') AND hub_group_tenant_link_id IS NOT NULL AND hub_tenant_relationship_id IS NULL)
    OR (scope = 'relationship.create' AND hub_tenant_relationship_id IS NOT NULL AND hub_group_tenant_link_id IS NULL)
  )
);

ALTER TABLE hub_group_organizer_receipts ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_group_organizer_receipts FORCE ROW LEVEL SECURITY;

-- A receipt is only ever read/written by its own actor's session (the service looks up its own retry,
-- never someone else's); no hub-wide or cross-actor visibility is needed.
CREATE POLICY hub_group_organizer_receipts_own ON hub_group_organizer_receipts FOR ALL
  USING (actor_id = current_user_id() OR is_system_admin())
  WITH CHECK (actor_id = current_user_id() OR is_system_admin());

GRANT SELECT, INSERT ON hub_group_organizer_receipts TO omnira_app;
