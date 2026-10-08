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

-- System role for Hub agents (hub_admin already exists since 000002). It carries NO tenant-scoped
-- permission: delegated tenant access comes only from effective_access_grants.
INSERT INTO roles (tenant_id, key, name)
SELECT NULL, 'hub_agent', 'Hub Agent'
WHERE NOT EXISTS (SELECT 1 FROM roles WHERE tenant_id IS NULL AND key = 'hub_agent');

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

  -- service_scope.queue_ids (optional JSON array of queue UUID strings): when the key is present
  -- it is an ALLOWLIST (an empty array means no queue); when absent the contract covers every queue.
  service_scope     JSONB NOT NULL DEFAULT '{}' CHECK (jsonb_typeof(service_scope) = 'object'),

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, tenant_id),
  -- target of the composite FK that pins a grant to the contract's own hub and tenant
  UNIQUE (id, hub_id, tenant_id),
  CHECK (valid_until IS NULL OR valid_until > valid_from)
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
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (id, hub_id)
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
  service_contract_id UUID NOT NULL,
  work_pool_id      UUID,

  -- capability: may this agent REPLY (claim and send) in the delegated tenant's conversations? Least privilege: read-only by default.
  can_reply         BOOLEAN NOT NULL DEFAULT false,

  -- grant validity (may differ from contract)
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),
  valid_from        TIMESTAMPTZ NOT NULL DEFAULT now(),
  valid_until       TIMESTAMPTZ,

  -- version for invalidation
  grant_version     BIGINT NOT NULL DEFAULT 1,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (hub_id, user_id, tenant_id, service_contract_id),
  CHECK (valid_until IS NULL OR valid_until > valid_from),

  -- a grant can only point at a contract of ITS OWN hub and ITS OWN tenant
  FOREIGN KEY (service_contract_id, hub_id, tenant_id)
    REFERENCES hub_tenant_service_contracts (id, hub_id, tenant_id) ON DELETE CASCADE,
  -- the grantee must be a member of the grant's hub; leaving the hub deletes the grant
  FOREIGN KEY (hub_id, user_id) REFERENCES hub_memberships (hub_id, user_id) ON DELETE CASCADE,
  -- a work pool, when set, must belong to the same hub
  FOREIGN KEY (work_pool_id, hub_id) REFERENCES work_pools (id, hub_id) ON DELETE SET NULL (work_pool_id)
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
  last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(), -- last message (or conversation start); the inbox sort key
  unread_count      INTEGER DEFAULT 0,

  metadata_json     JSONB DEFAULT '{}',

  version           BIGINT NOT NULL DEFAULT 1,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  -- tenant_id + conversation_id uniqueness (one inbox item per conversation per tenant)
  UNIQUE (hub_id, tenant_id, conversation_id),

  -- same composite-FK pattern the rest of the schema uses: the item can only point at a conversation
  -- of its OWN tenant, and disappears with it
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);

CREATE INDEX hub_inbox_items_hub_tenant_idx ON hub_inbox_items(hub_id, tenant_id);
CREATE INDEX hub_inbox_items_tenant_idx ON hub_inbox_items(tenant_id);
CREATE INDEX hub_inbox_items_activity_idx ON hub_inbox_items(hub_id, last_activity_at DESC, id DESC);
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
