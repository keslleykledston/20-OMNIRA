-- ADR-0028: Tenant Integration Gateway
-- Isolate and manage all external integrations (ERP, CRM, billing, NMS)
-- Each Integration Instance represents a unique connection per Tenant

CREATE TABLE integration_instances (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  provider          TEXT NOT NULL CHECK (char_length(provider) BETWEEN 1 AND 100),
  integration_type  TEXT NOT NULL CHECK (integration_type IN ('channel', 'crm', 'billing', 'nms', 'ticketing')),
  status            TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'inactive', 'error')),
  environment       TEXT DEFAULT 'prod' CHECK (environment IN ('prod', 'sandbox')),

  configuration_metadata JSONB DEFAULT '{}',
  credential_reference TEXT CHECK (char_length(credential_reference) <= 500), -- vault path, not secret

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_error_at     TIMESTAMPTZ,

  UNIQUE (tenant_id, provider, environment)
);

CREATE INDEX integration_instances_tenant_idx ON integration_instances(tenant_id);
CREATE INDEX integration_instances_status_idx ON integration_instances(status);
CREATE INDEX integration_instances_type_idx ON integration_instances(integration_type);

-- Integration capabilities: fine-grained permissions per instance
CREATE TABLE integration_capabilities (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  integration_instance_id UUID NOT NULL REFERENCES integration_instances(id) ON DELETE CASCADE,
  capability        TEXT NOT NULL CHECK (char_length(capability) BETWEEN 1 AND 100),
  enabled           BOOLEAN DEFAULT true,

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (integration_instance_id, capability)
);

CREATE INDEX integration_capabilities_instance_idx ON integration_capabilities(integration_instance_id);
CREATE INDEX integration_capabilities_enabled_idx ON integration_capabilities(enabled);

-- External action receipts: idempotency + audit for external writes (ADR-0029)
CREATE TABLE external_action_receipts (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  integration_instance_id UUID NOT NULL REFERENCES integration_instances(id) ON DELETE CASCADE,
  action_type       TEXT NOT NULL CHECK (char_length(action_type) BETWEEN 1 AND 100),

  external_id       TEXT, -- provider's resource ID (e.g., ticket #, order #)
  status            TEXT NOT NULL CHECK (status IN ('pending', 'in_flight', 'success', 'failure', 'reconciling')),

  actor_user_id     UUID NOT NULL REFERENCES users(id),
  hub_id            UUID REFERENCES service_hubs(id),
  conversation_id   UUID,

  correlation_id    TEXT NOT NULL CHECK (char_length(correlation_id) BETWEEN 1 AND 100),
  idempotency_key   TEXT NOT NULL CHECK (char_length(idempotency_key) BETWEEN 1 AND 100),

  sanitized_request_metadata JSONB DEFAULT '{}', -- no secrets
  sanitized_response_metadata JSONB DEFAULT '{}',

  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  confirmed_at      TIMESTAMPTZ,
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (tenant_id, integration_instance_id, idempotency_key)
);

CREATE INDEX external_action_receipts_tenant_idx ON external_action_receipts(tenant_id);
CREATE INDEX external_action_receipts_idempotency_idx ON external_action_receipts(idempotency_key);
CREATE INDEX external_action_receipts_status_idx ON external_action_receipts(status);
CREATE INDEX external_action_receipts_correlation_idx ON external_action_receipts(correlation_id);

-- Webhook deduplication: prevent replay attacks (ADR-0031)
CREATE TABLE webhook_deduplication (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  integration_instance_id UUID NOT NULL REFERENCES integration_instances(id) ON DELETE CASCADE,
  provider_event_id TEXT NOT NULL CHECK (char_length(provider_event_id) BETWEEN 1 AND 200),

  processed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),

  UNIQUE (integration_instance_id, provider_event_id)
);

CREATE INDEX webhook_dedup_instance_idx ON webhook_deduplication(integration_instance_id);

-- Grants
GRANT SELECT, INSERT, UPDATE, DELETE ON integration_instances TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON integration_capabilities TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON external_action_receipts TO omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON webhook_deduplication TO omnira_app;
