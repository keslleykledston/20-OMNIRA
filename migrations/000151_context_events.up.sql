-- ADR-0049 phase 2: the operational timeline. `context_events` is HISTORY: what happened, written once, never changed or deleted. The state of things lives
-- in the entities (service_sessions, later cases and tasks); a message is referred to by id, never copied; and nothing here is an AI summary.
--
-- A payload carries ids, enums and counts only (the application validates the shape and the size; the database caps the size too). The one free text, `note`,
-- is a reason a person typed (reopen, cancel) and is length-capped. Events are NOT copied to outbox_events: that table is for jobs (the publisher has a stream
-- only for `job.*`); delivering these to third parties reuses the headless webhook engine when it is merged (docs/service-context/06-events-webhooks.md).
SET LOCAL lock_timeout = '5s';

CREATE TABLE context_events (
  id              UUID PRIMARY KEY,
  tenant_id       UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  event_type      TEXT NOT NULL CHECK (event_type ~ '^[a-z_]+\.[a-z_]+$' AND char_length(event_type) <= 60),
  entity_type     TEXT NOT NULL CHECK (entity_type ~ '^[a-z_]{1,40}$'),
  entity_id       UUID NOT NULL,
  session_id      UUID,
  case_id         UUID,                      -- the foreign key is added with the cases table (000152)
  conversation_id UUID,
  contact_id      UUID,
  customer_id     UUID,
  actor_type      TEXT NOT NULL CHECK (actor_type IN ('user', 'system', 'flow', 'ai', 'integration')),
  actor_id        UUID,
  source_system   TEXT NOT NULL DEFAULT 'omnira' CHECK (source_system ~ '^[a-z0-9_.-]{1,40}$'),
  occurred_at     TIMESTAMPTZ NOT NULL,
  recorded_at     TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  schema_version  INTEGER NOT NULL DEFAULT 1 CHECK (schema_version >= 1),
  payload         JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (jsonb_typeof(payload) = 'object' AND octet_length(payload::text) <= 2048),
  note            TEXT NOT NULL DEFAULT '' CHECK (char_length(note) <= 500),
  correlation_id  UUID,
  causation_id    UUID,
  dedup_key       TEXT CHECK (dedup_key IS NULL OR char_length(dedup_key) BETWEEN 8 AND 200),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id)      REFERENCES service_sessions (tenant_id, id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id)      REFERENCES contacts (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id)     REFERENCES customer_accounts (tenant_id, id)
);
CREATE UNIQUE INDEX context_events_dedup_uq ON context_events (tenant_id, dedup_key) WHERE dedup_key IS NOT NULL;
CREATE INDEX context_events_session_idx  ON context_events (tenant_id, session_id, occurred_at, id) WHERE session_id IS NOT NULL;
CREATE INDEX context_events_case_idx     ON context_events (tenant_id, case_id, occurred_at, id) WHERE case_id IS NOT NULL;
CREATE INDEX context_events_contact_idx  ON context_events (tenant_id, contact_id, occurred_at DESC, id DESC) WHERE contact_id IS NOT NULL;
CREATE INDEX context_events_customer_idx ON context_events (tenant_id, customer_id, occurred_at DESC, id DESC) WHERE customer_id IS NOT NULL;

-- history is never CHANGED, by anybody (the owner role included): a trigger refuses every UPDATE. It is never DELETED by the application: the application role has
-- no DELETE privilege. There is deliberately no DELETE trigger: removing a whole company (its rows go with it, by the foreign key) must stay possible for the
-- owner role, and a trigger on DELETE would also fire for that cascade.
CREATE FUNCTION context_events_immutable() RETURNS trigger AS $$
BEGIN
  RAISE EXCEPTION 'context_events is append-only' USING ERRCODE = '23514';
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER context_events_no_update BEFORE UPDATE ON context_events FOR EACH ROW EXECUTE FUNCTION context_events_immutable();

ALTER TABLE context_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE context_events FORCE ROW LEVEL SECURITY;
CREATE POLICY context_events_read_tenant ON context_events FOR SELECT
  USING (is_system_admin() OR tenant_id IN (SELECT service_read_tenants()));
-- A person who holds ANY service key writes events, and only as THEMSELVES: actor_type 'user' and their own id. The other actor kinds (system, flow, ai, integration)
-- exist only in a system session (the worker, the flow engine, a Hub write path acting on behalf of somebody), so nobody can forge them with plain SQL.
CREATE POLICY context_events_insert_tenant ON context_events FOR INSERT
  WITH CHECK (is_system_admin()
    OR (actor_type = 'user' AND actor_id = current_user_id()
        AND (actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'service.update')
          OR actor_has_permission(tenant_id, current_user_id(), 'service.close') OR actor_has_permission(tenant_id, current_user_id(), 'service.assign')
          OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external') OR actor_has_permission(tenant_id, current_user_id(), 'service.request_external'))));
CREATE TRIGGER context_events_tenant_immutable BEFORE UPDATE OF tenant_id ON context_events FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
REVOKE UPDATE, DELETE ON context_events FROM omnira_app;
GRANT SELECT, INSERT ON context_events TO omnira_app;
