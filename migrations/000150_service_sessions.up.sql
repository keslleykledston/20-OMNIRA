-- ADR-0049 phase 1: service sessions (atendimentos) with a per-company protocol, the company's own catalogs (categories, results) and the
-- seven `service.*` permissions. ADDITIVE: no existing table is altered, no message is touched, nothing is converted from history.
--
--  * A session is a layer over a conversation (0 or 1 ACTIVE per conversation; many in sequence). The messages that belong to it are the ones of
--    the same conversation created inside one of its EPISODES [started_at, ended_at): nothing is copied or marked, and a trigger keeps the sessions of a
--    conversation disjoint. Reopening a session starts a new episode; the time it spent closed is not absorbed.
--  * The protocol comes from `service_counters` (one locked row per company and kind, taken in the same transaction that inserts the session): no
--    MAX()+1, no collision, and a rollback gives the number back.
--  * Writes ask the database for the KEY of the operation (read/create/update/close/assign...), not only for a membership: a trigger refines the
--    UPDATE per kind of change, and `version` is advanced by the database itself so no writer can skip the optimistic check.
--  * Reading follows the other operational tables (active membership). The Hub delegation (domain `service`) is migration 000154.
SET LOCAL lock_timeout = '5s';

INSERT INTO permissions (key, description) VALUES
  ('service.read',             'Read service sessions, cases, tasks and external records'),
  ('service.create',           'Open service sessions, cases and tasks'),
  ('service.update',           'Classify and edit service sessions, cases and tasks'),
  ('service.close',            'Close, abandon or reopen a service session; resolve or close a case; complete or cancel a task'),
  ('service.assign',           'Assign cases and tasks to people or teams'),
  ('service.link_external',    'Link an existing ERP/CRM record to a session, case or task'),
  ('service.request_external', 'Ask the ERP/CRM to create or change a record (never delegable to a Hub)')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions (role_id, permission_key)
SELECT r.id, p.key
FROM roles r
JOIN (VALUES
  ('tenant_admin',      'service.read'), ('tenant_admin',      'service.create'), ('tenant_admin',      'service.update'), ('tenant_admin',      'service.close'),
  ('tenant_admin',      'service.assign'), ('tenant_admin',    'service.link_external'), ('tenant_admin', 'service.request_external'),
  ('tenant_supervisor', 'service.read'), ('tenant_supervisor', 'service.create'), ('tenant_supervisor', 'service.update'), ('tenant_supervisor', 'service.close'),
  ('tenant_supervisor', 'service.assign'), ('tenant_supervisor', 'service.link_external'), ('tenant_supervisor', 'service.request_external'),
  ('tenant_agent',      'service.read'), ('tenant_agent',      'service.create'), ('tenant_agent',      'service.update'), ('tenant_agent',      'service.close'),
  ('tenant_agent',      'service.link_external')
) AS p(role_key, key) ON p.role_key = r.key
WHERE r.tenant_id IS NULL
ON CONFLICT DO NOTHING;

-- 1. protocol counters --------------------------------------------------------------------------------------------------------------------
CREATE TABLE service_counters (
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  kind       TEXT NOT NULL CHECK (kind IN ('session', 'case')),
  prefix     TEXT NOT NULL CHECK (prefix ~ '^[A-Z]{2,6}$'),
  last_value BIGINT NOT NULL CHECK (last_value >= 0),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, kind)
);

-- 2. the company's catalogs ---------------------------------------------------------------------------------------------------------------
CREATE TABLE service_categories (
  id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code       TEXT NOT NULL CHECK (code ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'),
  label      TEXT NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 80),
  active     BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code)
);

-- A company's own result code POINTS to one of the eight canonical ones, which are built in (they are not rows: nothing to seed, nothing to lose).
-- Reports aggregate by `canonical_code`. A company code cannot take a canonical name.
CREATE TABLE service_resolution_codes (
  id             UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id      UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  code           TEXT NOT NULL CHECK (code ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'
                                      AND code NOT IN ('information_provided', 'resolved_first_contact', 'task_created', 'case_created',
                                                       'external_service_order', 'transferred', 'abandoned', 'other')),
  canonical_code TEXT NOT NULL CHECK (canonical_code IN ('information_provided', 'resolved_first_contact', 'task_created', 'case_created',
                                                         'external_service_order', 'transferred', 'abandoned', 'other')),
  label          TEXT NOT NULL CHECK (char_length(btrim(label)) BETWEEN 1 AND 80),
  active         BOOLEAN NOT NULL DEFAULT true,
  created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, code)
);

-- 3. service sessions ---------------------------------------------------------------------------------------------------------------------
CREATE TABLE service_sessions (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  protocol            TEXT NOT NULL CHECK (protocol ~ '^[A-Z]{2,6}-[0-9]+$'),
  protocol_sequence   BIGINT NOT NULL CHECK (protocol_sequence >= 1),
  external_protocol   TEXT CHECK (external_protocol IS NULL OR char_length(btrim(external_protocol)) BETWEEN 1 AND 80),  -- a legacy/imported number, never in the generated range
  conversation_id     UUID,
  contact_id          UUID,
  customer_id         UUID,
  queue_id            UUID,                                                        -- the department: queues are the sectors (ADR-0043)
  category_id         UUID,
  status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'waiting', 'closed', 'abandoned')),
  resolution_code     TEXT CHECK (resolution_code IS NULL OR resolution_code ~ '^[a-z0-9][a-z0-9_.-]{0,39}$'),
  resolution_canonical TEXT CHECK (resolution_canonical IS NULL OR resolution_canonical IN ('information_provided', 'resolved_first_contact', 'task_created',
                                   'case_created', 'external_service_order', 'transferred', 'abandoned', 'other')),
  resolution_note     TEXT CHECK (resolution_note IS NULL OR char_length(resolution_note) <= 1000),
  origin_channel      TEXT NOT NULL DEFAULT 'other' CHECK (origin_channel ~ '^[a-z0-9_]{1,40}$'),
  opened_source       TEXT NOT NULL CHECK (opened_source IN ('operator', 'finalize', 'flow', 'api', 'system')),
  closed_source       TEXT CHECK (closed_source IS NULL OR closed_source IN ('operator', 'finalize', 'flow', 'api', 'system')),
  opened_by_user_id   UUID REFERENCES users(id),
  closed_by_user_id   UUID REFERENCES users(id),
  opened_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  first_response_at   TIMESTAMPTZ,
  closed_at           TIMESTAMPTZ,
  reopen_count        INTEGER NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
  idempotency_key     TEXT CHECK (idempotency_key IS NULL OR idempotency_key ~ '^[A-Za-z0-9._:-]{8,128}$'),
  request_hash        TEXT,
  version             INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, protocol),
  UNIQUE (tenant_id, protocol_sequence),
  CHECK ((status IN ('closed', 'abandoned')) = (closed_at IS NOT NULL)),
  CHECK ((status IN ('closed', 'abandoned')) = (resolution_code IS NOT NULL)),
  CHECK ((resolution_code IS NULL) = (resolution_canonical IS NULL)),
  CHECK (closed_at IS NULL OR closed_at >= opened_at),
  CHECK ((idempotency_key IS NULL) = (request_hash IS NULL)),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id)      REFERENCES contacts (tenant_id, id),
  FOREIGN KEY (tenant_id, customer_id)     REFERENCES customer_accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, queue_id)        REFERENCES queues (tenant_id, id),
  FOREIGN KEY (tenant_id, category_id)     REFERENCES service_categories (tenant_id, id)
);
-- at most ONE active session per conversation (many in sequence)
CREATE UNIQUE INDEX service_sessions_one_active_uq ON service_sessions (tenant_id, conversation_id)
  WHERE conversation_id IS NOT NULL AND status IN ('open', 'in_progress', 'waiting');
CREATE UNIQUE INDEX service_sessions_external_protocol_uq ON service_sessions (tenant_id, external_protocol) WHERE external_protocol IS NOT NULL;
CREATE UNIQUE INDEX service_sessions_idempotency_uq ON service_sessions (tenant_id, idempotency_key) WHERE idempotency_key IS NOT NULL;
CREATE INDEX service_sessions_conversation_idx ON service_sessions (tenant_id, conversation_id, opened_at DESC) WHERE conversation_id IS NOT NULL;
CREATE INDEX service_sessions_contact_idx  ON service_sessions (tenant_id, contact_id, opened_at DESC) WHERE contact_id IS NOT NULL;
CREATE INDEX service_sessions_customer_idx ON service_sessions (tenant_id, customer_id, opened_at DESC) WHERE customer_id IS NOT NULL;
CREATE INDEX service_sessions_status_idx   ON service_sessions (tenant_id, status, opened_at DESC, id DESC);
CREATE INDEX service_sessions_queue_idx    ON service_sessions (tenant_id, queue_id, opened_at DESC) WHERE queue_id IS NOT NULL;

-- 3b. episodes: the stretches of time a session was in course. A session has ONE episode until it is reopened; reopening starts another, so the
-- stretch it spent closed is never absorbed (the messages of that gap belong to no session). A message belongs to the session when its created_at falls in
-- one of its episodes: [started_at, ended_at), half-open, so an instant belongs to exactly one side.
CREATE TABLE service_session_episodes (
  id         UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  session_id UUID NOT NULL,
  seq        INTEGER NOT NULL CHECK (seq >= 1),
  started_at TIMESTAMPTZ NOT NULL,
  ended_at   TIMESTAMPTZ,
  UNIQUE (tenant_id, session_id, seq),
  CHECK (ended_at IS NULL OR ended_at >= started_at),
  FOREIGN KEY (tenant_id, session_id) REFERENCES service_sessions (tenant_id, id)
);
CREATE UNIQUE INDEX service_session_episodes_one_open_uq ON service_session_episodes (tenant_id, session_id) WHERE ended_at IS NULL;
CREATE INDEX service_session_episodes_range_idx ON service_session_episodes (tenant_id, session_id, started_at);

-- 4. row guards ---------------------------------------------------------------------------------------------------------------------------
-- A new session of a conversation opens only AFTER the previous one closed: the intervals that decide which messages belong to a session stay disjoint.
CREATE FUNCTION service_sessions_before_insert() RETURNS trigger AS $$
BEGIN
  IF NEW.conversation_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM public.service_sessions s
       WHERE s.tenant_id = NEW.tenant_id AND s.conversation_id = NEW.conversation_id
         AND (s.closed_at IS NULL OR s.closed_at > NEW.opened_at)) THEN
    RAISE EXCEPTION 'a service session of this conversation is still open or ends after this one starts' USING ERRCODE = '23P01';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_sessions_before_insert_trg BEFORE INSERT ON service_sessions FOR EACH ROW EXECUTE FUNCTION service_sessions_before_insert();

-- UPDATE: the identity of the row never moves; the version and the clock are advanced HERE (so no writer can skip the optimistic check); and the
-- key asked of the person depends on WHAT changes (closing/reopening = service.close, anything else = service.update). The system session
-- (flows, finalize on behalf of the system) is the one exception: it is not a person.
CREATE FUNCTION service_sessions_before_update() RETURNS trigger AS $$
DECLARE need TEXT;
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.protocol IS DISTINCT FROM OLD.protocol
     OR NEW.protocol_sequence IS DISTINCT FROM OLD.protocol_sequence OR NEW.conversation_id IS DISTINCT FROM OLD.conversation_id
     OR NEW.opened_at IS DISTINCT FROM OLD.opened_at OR NEW.opened_source IS DISTINCT FROM OLD.opened_source
     OR NEW.opened_by_user_id IS DISTINCT FROM OLD.opened_by_user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'the identity of a service session never changes' USING ERRCODE = '23514';
  END IF;
  IF (OLD.status IN ('closed', 'abandoned')) <> (NEW.status IN ('closed', 'abandoned')) THEN need := 'service.close'; ELSE need := 'service.update'; END IF;
  IF NOT public.is_system_admin() AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), need) THEN
    RAISE EXCEPTION 'service session: % required', need USING ERRCODE = '42501';
  END IF;
  -- only the LATEST session of a conversation can be reopened (the intervals of the later ones must stay theirs)
  IF OLD.status IN ('closed', 'abandoned') AND NEW.status NOT IN ('closed', 'abandoned') AND NEW.conversation_id IS NOT NULL AND EXISTS (
       SELECT 1 FROM public.service_sessions s
       WHERE s.tenant_id = NEW.tenant_id AND s.conversation_id = NEW.conversation_id AND s.id <> NEW.id AND s.opened_at >= OLD.opened_at) THEN
    RAISE EXCEPTION 'only the latest service session of a conversation can be reopened' USING ERRCODE = '23514';
  END IF;
  NEW.version := OLD.version + 1;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_sessions_before_update_trg BEFORE UPDATE ON service_sessions FOR EACH ROW EXECUTE FUNCTION service_sessions_before_update();

-- 5. RLS ----------------------------------------------------------------------------------------------------------------------------------
DO $$
DECLARE t TEXT; w TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['service_counters', 'service_categories', 'service_resolution_codes', 'service_sessions', 'service_session_episodes'] LOOP
    w := CASE t
           WHEN 'service_session_episodes' THEN '(actor_has_permission(tenant_id, current_user_id(), ''service.create'') OR actor_has_permission(tenant_id, current_user_id(), ''service.close''))'
           WHEN 'service_counters'         THEN 'actor_has_permission(tenant_id, current_user_id(), ''service.create'')'
           WHEN 'service_sessions'         THEN 'actor_has_permission(tenant_id, current_user_id(), ''service.create'')'
           ELSE                                 'actor_has_permission(tenant_id, current_user_id(), ''tenant.manage'')' END;
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (is_system_admin() OR %s)', t || '_insert_tenant', t, w);
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
  END LOOP;
  -- UPDATE: the key of the operation is asked again by the row trigger; the policy only opens the door to people who hold ANY key that can change the row.
  CREATE POLICY service_counters_update_tenant ON service_counters FOR UPDATE
    USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create'))
    WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create'));
  CREATE POLICY service_categories_update_tenant ON service_categories FOR UPDATE
    USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'))
    WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
  CREATE POLICY service_resolution_codes_update_tenant ON service_resolution_codes FOR UPDATE
    USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'))
    WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'tenant.manage'));
  CREATE POLICY service_session_episodes_update_tenant ON service_session_episodes FOR UPDATE
    USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'service.close'))
    WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.create') OR actor_has_permission(tenant_id, current_user_id(), 'service.close'));
  CREATE POLICY service_sessions_update_tenant ON service_sessions FOR UPDATE
    USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close'))
    WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close'));
END $$;
-- nothing here is ever deleted by the application: sessions are a record; catalog entries are deactivated
-- (the default privileges of migration 000006 hand DELETE to every new table, so it is taken back explicitly)
REVOKE DELETE ON service_counters, service_categories, service_resolution_codes, service_sessions, service_session_episodes FROM omnira_app;
GRANT SELECT, INSERT, UPDATE ON service_counters, service_categories, service_resolution_codes, service_sessions, service_session_episodes TO omnira_app;
