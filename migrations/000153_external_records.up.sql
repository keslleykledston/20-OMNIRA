-- ADR-0049 phase 4: external records (ERP/CRM). The ERP is the source of truth of a service order (ADR-06): OMNIRA never makes an order of its own; it keeps a REFERENCE to
-- the ERP's record, a minimal projection of its state, and the history of how it got linked. Nothing here calls the ERP; that is the port the application owns.
--   * external_records: one row per ERP record the company cares about (unique by integration + type + id), with a normalized status class and a sync state. The
--     provider's own update time is kept and can only move FORWARD, so an update that arrives out of order never rolls the projection back.
--   * service_external_links: which session, case or task a record belongs to (exactly one), soft-unlinked, never deleted.
--   * external_record_requests: the durable attempt to ask the ERP to create an order. Same safety model as the ticket attempts (ADR-0013): a timeout is NOT a failure,
--     it is `outcome_unknown` and BLOCKS a new request for the session until somebody reconciles it.
SET LOCAL lock_timeout = '5s';

CREATE TABLE external_records (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  integration_id     UUID NOT NULL,                                            -- the ERP/CRM connection of the company (channel_connections)
  external_system    TEXT NOT NULL CHECK (external_system ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'),
  external_type      TEXT NOT NULL CHECK (external_type ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'),
  external_id        TEXT NOT NULL CHECK (char_length(btrim(external_id)) BETWEEN 1 AND 120),
  display_ref        TEXT NOT NULL DEFAULT '' CHECK (char_length(display_ref) <= 120),   -- "OS 1042": what an attendant reads; never customer data
  external_status    TEXT NOT NULL DEFAULT '' CHECK (char_length(external_status) <= 80),
  status_class       TEXT NOT NULL DEFAULT 'unknown' CHECK (status_class IN ('open', 'in_progress', 'done', 'cancelled', 'unknown')),
  sync_state         TEXT NOT NULL DEFAULT 'pending' CHECK (sync_state IN ('synced', 'pending', 'stale', 'conflict', 'error')),
  source_updated_at  TIMESTAMPTZ,                                              -- the PROVIDER's update time of the snapshot applied last
  last_synced_at     TIMESTAMPTZ,
  created_by_user_id UUID REFERENCES users(id),
  version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, integration_id, external_type, external_id),
  FOREIGN KEY (tenant_id, integration_id) REFERENCES channel_connections (tenant_id, id)
);
CREATE INDEX external_records_status_idx ON external_records (tenant_id, status_class, updated_at DESC);

CREATE TABLE service_external_links (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  external_record_id  UUID NOT NULL,
  session_id          UUID,
  case_id             UUID,
  task_id             UUID,
  linked_by_user_id   UUID REFERENCES users(id),
  linked_at           TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  unlinked_at         TIMESTAMPTZ,
  unlinked_by_user_id UUID REFERENCES users(id),
  UNIQUE (tenant_id, id),
  CHECK (((session_id IS NOT NULL)::int + (case_id IS NOT NULL)::int + (task_id IS NOT NULL)::int) = 1),
  CHECK ((unlinked_at IS NULL) = (unlinked_by_user_id IS NULL) OR unlinked_by_user_id IS NULL),
  FOREIGN KEY (tenant_id, external_record_id) REFERENCES external_records (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id)         REFERENCES service_sessions (tenant_id, id),
  FOREIGN KEY (tenant_id, case_id)            REFERENCES service_cases (tenant_id, id),
  FOREIGN KEY (tenant_id, task_id)            REFERENCES service_tasks (tenant_id, id)
);
CREATE UNIQUE INDEX service_external_links_session_uq ON service_external_links (tenant_id, external_record_id, session_id) WHERE session_id IS NOT NULL AND unlinked_at IS NULL;
CREATE UNIQUE INDEX service_external_links_case_uq    ON service_external_links (tenant_id, external_record_id, case_id)    WHERE case_id IS NOT NULL    AND unlinked_at IS NULL;
CREATE UNIQUE INDEX service_external_links_task_uq    ON service_external_links (tenant_id, external_record_id, task_id)    WHERE task_id IS NOT NULL    AND unlinked_at IS NULL;
CREATE INDEX service_external_links_record_idx  ON service_external_links (tenant_id, external_record_id);
CREATE INDEX service_external_links_session_idx ON service_external_links (tenant_id, session_id) WHERE session_id IS NOT NULL;
CREATE INDEX service_external_links_case_idx    ON service_external_links (tenant_id, case_id) WHERE case_id IS NOT NULL;
CREATE INDEX service_external_links_task_idx    ON service_external_links (tenant_id, task_id) WHERE task_id IS NOT NULL;

CREATE TABLE external_record_requests (
  id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  integration_id       UUID NOT NULL,
  session_id           UUID NOT NULL,
  case_id              UUID,
  operation            TEXT NOT NULL CHECK (operation IN ('create_service_order')),
  title                TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 120),
  idempotency_key      TEXT NOT NULL CHECK (idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash         TEXT NOT NULL,
  correlation_key      TEXT NOT NULL CHECK (char_length(correlation_key) BETWEEN 8 AND 120),   -- sent to the ERP so a record can be found again when the answer was lost
  state                TEXT NOT NULL DEFAULT 'in_flight' CHECK (state IN ('in_flight', 'confirmed_success', 'confirmed_failure', 'outcome_unknown')),
  external_record_id   UUID,
  failure_class        TEXT CHECK (failure_class IS NULL OR failure_class ~ '^[a-z0-9_]{1,40}$'),
  resolution_source    TEXT CHECK (resolution_source IS NULL OR resolution_source IN ('gateway', 'reconcile', 'manual')),
  requested_by_user_id UUID REFERENCES users(id),
  resolved_by_user_id  UUID REFERENCES users(id),
  resolved_at          TIMESTAMPTZ,
  version              INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at           TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, idempotency_key),
  UNIQUE (tenant_id, correlation_key),
  CHECK ((state = 'confirmed_success') = (external_record_id IS NOT NULL)),
  CHECK ((state IN ('confirmed_success', 'confirmed_failure')) = (resolved_at IS NOT NULL)),
  CHECK ((state = 'confirmed_failure') = (failure_class IS NOT NULL) OR state = 'outcome_unknown'),
  FOREIGN KEY (tenant_id, integration_id)    REFERENCES channel_connections (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id)        REFERENCES service_sessions (tenant_id, id),
  FOREIGN KEY (tenant_id, case_id)           REFERENCES service_cases (tenant_id, id),
  FOREIGN KEY (tenant_id, external_record_id) REFERENCES external_records (tenant_id, id)
);
-- a session has at most ONE request that is in flight or of unknown outcome: asking again while the first may have been processed would duplicate the order
CREATE UNIQUE INDEX external_record_requests_blocking_uq ON external_record_requests (tenant_id, session_id, operation) WHERE state IN ('in_flight', 'outcome_unknown');
CREATE INDEX external_record_requests_state_idx ON external_record_requests (tenant_id, state, created_at) WHERE state IN ('in_flight', 'outcome_unknown');

-- the idempotency receipts also cover the inbound updates of the ERP (one delivery id, applied once) and the steps of a flow that act on a session
ALTER TABLE service_command_receipts DROP CONSTRAINT service_command_receipts_scope_check;
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_scope_check CHECK (scope IN ('session.open', 'case.open', 'task.create', 'external.update', 'flow.step'));
ALTER TABLE service_command_receipts ADD COLUMN external_record_id UUID;
ALTER TABLE service_command_receipts ADD FOREIGN KEY (tenant_id, external_record_id) REFERENCES external_records (tenant_id, id);
ALTER TABLE service_command_receipts DROP CONSTRAINT service_command_receipts_target;
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_target CHECK (
  (scope = 'session.open' AND session_id IS NOT NULL AND case_id IS NULL AND task_id IS NULL AND external_record_id IS NULL)
  OR (scope = 'case.open' AND case_id IS NOT NULL AND session_id IS NULL AND task_id IS NULL AND external_record_id IS NULL)
  OR (scope = 'task.create' AND task_id IS NOT NULL AND session_id IS NULL AND case_id IS NULL AND external_record_id IS NULL)
  OR (scope = 'external.update' AND external_record_id IS NOT NULL AND session_id IS NULL AND case_id IS NULL AND task_id IS NULL)
  -- a flow step that acted on a session (classify, close): the step is done, whatever session is active when it is replayed
  OR (scope = 'flow.step' AND session_id IS NOT NULL AND case_id IS NULL AND task_id IS NULL AND external_record_id IS NULL));

-- row guards -------------------------------------------------------------------------------------------------------------------------------
-- A record: identity immutable; version and clock advanced here; the provider's update time only moves FORWARD (an out-of-order update never rolls the projection
-- back); changes need service.link_external (the key of tying an ERP record to our work; asking the ERP for something is a different key, on a different table). The
-- system session, which applies the ERP's own updates, is exempt from the KEYS only.
CREATE FUNCTION external_records_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.integration_id IS DISTINCT FROM OLD.integration_id
     OR NEW.external_system IS DISTINCT FROM OLD.external_system OR NEW.external_type IS DISTINCT FROM OLD.external_type
     OR NEW.external_id IS DISTINCT FROM OLD.external_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'the identity of an external record never changes' USING ERRCODE = '23514';
  END IF;
  IF OLD.source_updated_at IS NOT NULL AND (NEW.source_updated_at IS NULL OR NEW.source_updated_at < OLD.source_updated_at) THEN
    RAISE EXCEPTION 'an external record never goes back in time' USING ERRCODE = '23514';
  END IF;
  IF NOT public.is_system_admin() AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.link_external') THEN
    RAISE EXCEPTION 'external record: service.link_external required' USING ERRCODE = '42501';
  END IF;
  NEW.version := OLD.version + 1;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER external_records_before_update_trg BEFORE UPDATE ON external_records FOR EACH ROW EXECUTE FUNCTION external_records_before_update();

-- A link only ever gets its unlinking, once.
CREATE FUNCTION service_external_links_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.external_record_id IS DISTINCT FROM OLD.external_record_id
     OR NEW.session_id IS DISTINCT FROM OLD.session_id OR NEW.case_id IS DISTINCT FROM OLD.case_id OR NEW.task_id IS DISTINCT FROM OLD.task_id
     OR NEW.linked_by_user_id IS DISTINCT FROM OLD.linked_by_user_id OR NEW.linked_at IS DISTINCT FROM OLD.linked_at
     OR OLD.unlinked_at IS NOT NULL OR NEW.unlinked_at IS NULL THEN
    RAISE EXCEPTION 'a link only gets its unlinking, once' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_external_links_before_update_trg BEFORE UPDATE ON service_external_links FOR EACH ROW EXECUTE FUNCTION service_external_links_before_update();

-- A request: identity immutable; the states move only forward (in_flight -> any; outcome_unknown -> a confirmed one; confirmed is final); every change needs
-- service.request_external; version advanced here.
CREATE FUNCTION external_record_requests_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.integration_id IS DISTINCT FROM OLD.integration_id
     OR NEW.session_id IS DISTINCT FROM OLD.session_id OR NEW.case_id IS DISTINCT FROM OLD.case_id OR NEW.operation IS DISTINCT FROM OLD.operation
     OR NEW.title IS DISTINCT FROM OLD.title OR NEW.idempotency_key IS DISTINCT FROM OLD.idempotency_key OR NEW.request_hash IS DISTINCT FROM OLD.request_hash
     OR NEW.correlation_key IS DISTINCT FROM OLD.correlation_key OR NEW.requested_by_user_id IS DISTINCT FROM OLD.requested_by_user_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'the identity of an external request never changes' USING ERRCODE = '23514';
  END IF;
  IF OLD.state IN ('confirmed_success', 'confirmed_failure') THEN
    RAISE EXCEPTION 'a confirmed request is final' USING ERRCODE = '23514';
  END IF;
  IF NEW.state = 'in_flight' AND OLD.state <> 'in_flight' THEN
    RAISE EXCEPTION 'a request does not go back to in flight' USING ERRCODE = '23514';
  END IF;
  IF NOT public.is_system_admin() AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.request_external') THEN
    RAISE EXCEPTION 'external request: service.request_external required' USING ERRCODE = '42501';
  END IF;
  NEW.version := OLD.version + 1;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER external_record_requests_before_update_trg BEFORE UPDATE ON external_record_requests FOR EACH ROW EXECUTE FUNCTION external_record_requests_before_update();

-- RLS --------------------------------------------------------------------------------------------------------------------------------------
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['external_records', 'service_external_links', 'external_record_requests'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (is_system_admin() OR tenant_id IN (SELECT service_read_tenants()))', t || '_read_tenant', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
  END LOOP;
END $$;
-- records and links are written with service.link_external ONLY; the table of requests to the ERP is written with service.request_external ONLY. Asking the ERP and
-- keeping what it answered therefore needs both keys (the application asks for both), and holding one gives no way to forge the other's rows with plain SQL.
CREATE POLICY external_records_insert_tenant ON external_records FOR INSERT WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'));
CREATE POLICY external_records_update_tenant ON external_records FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'));
CREATE POLICY service_external_links_insert_tenant ON service_external_links FOR INSERT WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'));
CREATE POLICY service_external_links_update_tenant ON service_external_links FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.link_external'));
CREATE POLICY external_record_requests_insert_tenant ON external_record_requests FOR INSERT WITH CHECK (is_system_admin() OR (
  actor_has_permission(tenant_id, current_user_id(), 'service.request_external') AND state = 'in_flight'));
CREATE POLICY external_record_requests_update_tenant ON external_record_requests FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.request_external'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.request_external'));
REVOKE DELETE ON external_records, service_external_links, external_record_requests FROM omnira_app;
GRANT SELECT, INSERT, UPDATE ON external_records, service_external_links, external_record_requests TO omnira_app;

-- a receipt for an inbound ERP update (the write policy of the table is service.create/update; an inbound delivery is applied by the system session)
