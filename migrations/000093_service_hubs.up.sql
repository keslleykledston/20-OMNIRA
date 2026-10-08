-- ADR-0025: Service Hub delegation model
-- Enables BPO/delegated operations via Hub membership + Tenant service contracts
CREATE TABLE service_hubs (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  name              TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  description       TEXT DEFAULT '' CHECK (char_length(description) <= 1000),
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended')),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- Hub memberships: users who operate within a hub
CREATE TABLE hub_memberships (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  role_id           UUID NOT NULL REFERENCES roles(id),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, user_id)
);

CREATE INDEX hub_memberships_user_idx ON hub_memberships(user_id);
CREATE INDEX hub_memberships_hub_idx ON hub_memberships(hub_id);

-- Service contracts: which Tenant delegated to which Hub
CREATE TABLE hub_tenant_service_contracts (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),

  valid_from        TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until       TIMESTAMPTZ,

  service_scope     JSONB DEFAULT '{}', -- queues, capabilities, business_hours, etc.

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, tenant_id)
);

CREATE INDEX hub_tenant_service_contracts_tenant_idx ON hub_tenant_service_contracts(tenant_id);
CREATE INDEX hub_tenant_service_contracts_hub_idx ON hub_tenant_service_contracts(hub_id);
CREATE INDEX hub_tenant_service_contracts_status_idx ON hub_tenant_service_contracts(status) WHERE status = 'active';

-- Work pools: groups of agents within a hub by capability/specialty
CREATE TABLE work_pools (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  name              TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  description       TEXT DEFAULT '' CHECK (char_length(description) <= 1000),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX work_pools_hub_idx ON work_pools(hub_id);

-- Work pool membership: agents assigned to pools
CREATE TABLE work_pool_members (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  work_pool_id      UUID NOT NULL REFERENCES work_pools(id) ON DELETE CASCADE,
  user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (work_pool_id, user_id)
);

CREATE INDEX work_pool_members_user_idx ON work_pool_members(user_id);
CREATE INDEX work_pool_members_pool_idx ON work_pool_members(work_pool_id);

-- Skills library: what agents can do
CREATE TABLE skills (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  key               TEXT NOT NULL CHECK (char_length(key) BETWEEN 1 AND 100),
  name              TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 200),
  description       TEXT DEFAULT '' CHECK (char_length(description) <= 1000),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, key)
);

CREATE INDEX skills_hub_idx ON skills(hub_id);

-- Agent skills: mapping agents to skills
CREATE TABLE agent_skills (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  skill_id          UUID NOT NULL REFERENCES skills(id) ON DELETE CASCADE,
  proficiency       TEXT DEFAULT 'basic' CHECK (proficiency IN ('basic', 'intermediate', 'advanced')),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (user_id, skill_id)
);

CREATE INDEX agent_skills_user_idx ON agent_skills(user_id);
CREATE INDEX agent_skills_skill_idx ON agent_skills(skill_id);

-- Effective access grants: cached projection of what an agent can access
-- This is NOT a permission matrix; it's a denormalized view of the derivation:
--   user → hub membership + skill → work pool → service contract → effective grant
CREATE TABLE effective_access_grants (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  user_id           UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  service_contract_id UUID NOT NULL REFERENCES hub_tenant_service_contracts(id) ON DELETE CASCADE,

  work_pool_id      UUID REFERENCES work_pools(id) ON DELETE CASCADE,

  -- grant validity (may differ from contract)
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),
  valid_from        TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until       TIMESTAMPTZ,

  -- version for invalidation
  grant_version     BIGINT NOT NULL DEFAULT 1,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, user_id, tenant_id, service_contract_id)
);

CREATE INDEX effective_access_grants_user_tenant_idx ON effective_access_grants(user_id, tenant_id);
CREATE INDEX effective_access_grants_hub_idx ON effective_access_grants(hub_id);
CREATE INDEX effective_access_grants_contract_idx ON effective_access_grants(service_contract_id);
CREATE INDEX effective_access_grants_status_idx ON effective_access_grants(status) WHERE status = 'active';

-- Hub Inbox: unified work queue for Hub agents (event-driven projection)
-- Source of truth is tenant conversations; this is the read model.
CREATE TABLE hub_inbox_items (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id            UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id   UUID NOT NULL,

  queue_id          UUID,          -- which queue this conversation is in
  assigned_user_id  UUID,          -- which agent (if assigned)

  customer_name     TEXT DEFAULT '',
  channel           TEXT DEFAULT '',
  status            TEXT DEFAULT 'open',
  priority          TEXT DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),

  sla_due_at        TIMESTAMPTZ,
  last_activity_at  TIMESTAMPTZ,
  unread_count      INTEGER DEFAULT 0,

  metadata_json     JSONB DEFAULT '{}',

  version           BIGINT NOT NULL DEFAULT 1,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- tenant_id + conversation_id uniqueness (one inbox item per conversation per tenant)
  UNIQUE (hub_id, tenant_id, conversation_id)
);

CREATE INDEX hub_inbox_items_hub_tenant_idx ON hub_inbox_items(hub_id, tenant_id);
CREATE INDEX hub_inbox_items_tenant_idx ON hub_inbox_items(tenant_id);
CREATE INDEX hub_inbox_items_updated_at_idx ON hub_inbox_items(updated_at DESC) WHERE status != 'closed';
CREATE INDEX hub_inbox_items_assigned_idx ON hub_inbox_items(assigned_user_id) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX hub_inbox_items_sla_idx ON hub_inbox_items(sla_due_at) WHERE sla_due_at IS NOT NULL AND status != 'closed';

-- Grants for app
GRANT SELECT, INSERT, UPDATE, DELETE ON service_hubs TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON hub_memberships TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON hub_tenant_service_contracts TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON work_pools TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON work_pool_members TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON skills TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON agent_skills TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON effective_access_grants TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON hub_inbox_items TO omnira_app;
