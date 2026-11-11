-- ADR-0059: Hub group organizer — catalog of WhatsApp group metadata and contextual tenant links.
--
-- This is a NEW, Hub-scoped layer. It never touches wa_groups/wa_group_messages/wa_group_archive_batches
-- (ADR-0040 decided 2026-10-09 that those stay "member only"; this migration does not revisit that).
-- Authorization reuses the existing delegated-management mechanism of ADR-0038/migration 000103
-- (hub_tenant_service_contracts.management_scopes + effective_access_grants.can_manage +
-- has_hub_manage_access()) instead of inventing a new permission family: a new scope value 'groups'
-- is added to the scopes a contract may delegate. Nothing is granted by adding the value to the
-- allowed list; a contract only gets it if someone explicitly puts 'groups' in its management_scopes.

ALTER TABLE hub_tenant_service_contracts DROP CONSTRAINT hub_tenant_service_contracts_management_scopes_check;
ALTER TABLE hub_tenant_service_contracts
  ADD CONSTRAINT hub_tenant_service_contracts_management_scopes_check
    CHECK (management_scopes <@ ARRAY['channels', 'integrations', 'team', 'queues', 'settings', 'groups']::TEXT[]);

-- One catalog entry per group identity as seen through ONE tenant's ONE connection. Never merged
-- automatically across connections/tenants (D-03): identity is the full composite key below.
CREATE TABLE hub_group_catalog (
  id                            UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id                        UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  source_tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  source_channel_connection_id  UUID NOT NULL,
  provider_group_id             TEXT NOT NULL CHECK (provider_group_id ~ '^[0-9]{5,20}(-[0-9]{1,20})?@g\.us$'),
  display_name                  TEXT NOT NULL DEFAULT '' CHECK (char_length(display_name) <= 200),
  source_state                  TEXT NOT NULL DEFAULT 'available'
                                   CHECK (source_state IN ('available', 'stale', 'unavailable', 'archived')),
  version                       BIGINT NOT NULL DEFAULT 1,
  created_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at                    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at                  TIMESTAMPTZ NOT NULL DEFAULT now(),

  CONSTRAINT hub_group_catalog_hub_uq UNIQUE (id, hub_id),
  CONSTRAINT hub_group_catalog_identity_uq
    UNIQUE (hub_id, source_tenant_id, source_channel_connection_id, provider_group_id),
  -- mirrors wa_groups_connection_fk: the connection in the catalog row must really belong to the
  -- tenant claimed as its source, so a forged source_channel_connection_id of another tenant fails.
  CONSTRAINT hub_group_catalog_connection_fk FOREIGN KEY (source_tenant_id, source_channel_connection_id)
    REFERENCES channel_connections (tenant_id, id) ON DELETE RESTRICT
);
CREATE INDEX hub_group_catalog_hub_idx ON hub_group_catalog (hub_id);
CREATE INDEX hub_group_catalog_source_tenant_idx ON hub_group_catalog (source_tenant_id);

ALTER TABLE hub_group_catalog ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_group_catalog FORCE ROW LEVEL SECURITY;

-- Only a Hub member with 'groups' delegated AND can_manage/hub_admin FOR THE ORIGIN TENANT can see or
-- change a catalog entry. This is metadata only (name, state); it never implies wa_groups.enabled and
-- never grants any read of wa_group_messages.
CREATE POLICY hub_group_catalog_rw ON hub_group_catalog FOR ALL
  USING (has_hub_manage_access(source_tenant_id, current_user_id(), 'groups', hub_id))
  WITH CHECK (has_hub_manage_access(source_tenant_id, current_user_id(), 'groups', hub_id));

GRANT SELECT, INSERT, UPDATE, DELETE ON hub_group_catalog TO omnira_app;

-- N:N catalog entry <-> tenant, with a role. D-06 (approved): exactly one ACTIVE link per
-- (group, tenant) in this MVP; multiple roles/services per tenant is a future, separate decision.
CREATE TABLE hub_group_tenant_links (
  id              UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id          UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  hub_group_id    UUID NOT NULL,
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  role            TEXT NOT NULL CHECK (role IN ('consumer', 'supplier', 'operator', 'partner', 'observer')),
  status          TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'revoked')),
  assigned_by     UUID REFERENCES users(id) ON DELETE SET NULL,
  assigned_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  revoked_by      UUID REFERENCES users(id) ON DELETE SET NULL,
  revoked_at      TIMESTAMPTZ,
  version         BIGINT NOT NULL DEFAULT 1,
  created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- a link's group must belong to the SAME hub as the link itself (never link a group cataloged by
  -- another Hub), mirroring the work_pools (id, hub_id) pattern of migration 000093.
  CONSTRAINT hub_group_tenant_links_group_fk FOREIGN KEY (hub_group_id, hub_id)
    REFERENCES hub_group_catalog (id, hub_id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX hub_group_tenant_links_one_active_per_tenant_uq
  ON hub_group_tenant_links (hub_group_id, tenant_id) WHERE status = 'active';
CREATE INDEX hub_group_tenant_links_hub_idx ON hub_group_tenant_links (hub_id);
CREATE INDEX hub_group_tenant_links_tenant_idx ON hub_group_tenant_links (tenant_id);
CREATE INDEX hub_group_tenant_links_group_idx ON hub_group_tenant_links (hub_group_id);

ALTER TABLE hub_group_tenant_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_group_tenant_links FORCE ROW LEVEL SECURITY;

-- Row-level, not group-level: an actor authorized only for Alpha never sees (nor counts) the SAME
-- group's link to Beta, even though they can see the catalog entry itself (RF-13/SEC-013).
CREATE POLICY hub_group_tenant_links_rw ON hub_group_tenant_links FOR ALL
  USING (has_hub_manage_access(tenant_id, current_user_id(), 'groups', hub_id))
  WITH CHECK (has_hub_manage_access(tenant_id, current_user_id(), 'groups', hub_id));

GRANT SELECT, INSERT, UPDATE, DELETE ON hub_group_tenant_links TO omnira_app;

-- Commercial relationship between two tenants under the same hub (e.g. Alpha consumes a service from
-- Beta). Independent of any group link; D-07 (approved): shared folders in the UI are ALWAYS derived
-- from this table, never a manually-created arbitrary collection.
CREATE TABLE hub_tenant_relationships (
  id                    UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id                UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  consumer_tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  supplier_tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  service_kind          TEXT NOT NULL CHECK (char_length(service_kind) BETWEEN 1 AND 100),
  service_reference     TEXT NOT NULL DEFAULT '' CHECK (char_length(service_reference) <= 200),
  status                TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),
  valid_from            TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until           TIMESTAMPTZ,
  created_by            UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at            TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at            TIMESTAMPTZ NOT NULL DEFAULT now(),

  CHECK (consumer_tenant_id <> supplier_tenant_id),
  CHECK (valid_until IS NULL OR valid_until > valid_from)
);
CREATE UNIQUE INDEX hub_tenant_relationships_active_uq
  ON hub_tenant_relationships (hub_id, consumer_tenant_id, supplier_tenant_id, service_kind)
  WHERE status = 'active';
CREATE INDEX hub_tenant_relationships_hub_idx ON hub_tenant_relationships (hub_id);
CREATE INDEX hub_tenant_relationships_consumer_idx ON hub_tenant_relationships (consumer_tenant_id);
CREATE INDEX hub_tenant_relationships_supplier_idx ON hub_tenant_relationships (supplier_tenant_id);

ALTER TABLE hub_tenant_relationships ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_tenant_relationships FORCE ROW LEVEL SECURITY;

-- Visible only to someone authorized for BOTH sides (SEC-002): an actor who only has Alpha must not
-- learn that a relationship with Beta even exists.
CREATE POLICY hub_tenant_relationships_rw ON hub_tenant_relationships FOR ALL
  USING (
    has_hub_manage_access(consumer_tenant_id, current_user_id(), 'groups', hub_id)
    AND has_hub_manage_access(supplier_tenant_id, current_user_id(), 'groups', hub_id)
  )
  WITH CHECK (
    has_hub_manage_access(consumer_tenant_id, current_user_id(), 'groups', hub_id)
    AND has_hub_manage_access(supplier_tenant_id, current_user_id(), 'groups', hub_id)
  );

GRANT SELECT, INSERT, UPDATE, DELETE ON hub_tenant_relationships TO omnira_app;
