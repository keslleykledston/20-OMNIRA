-- ADR-0049 phase 3: cases (ocorrências) and tasks. Both are INDEPENDENT of the conversation and of each other (ADR-04, ADR-05, ADR-10):
--   * a case outlives the attendance that opened it and many sessions can refer to it (service_case_sessions); it is never created for a conversation by itself;
--   * a task is a pending action that can be born from a session, a case, a flow, a person, the AI (only by approval) or the API, with or without a case;
--   * finishing a task never resolves a case, closing a session never closes a case or a task, and cancelling either needs a reason.
-- The same discipline as 000150: the database asks for the key of each KIND of change, advances `version` itself, keeps identity immutable and deletes nothing.
SET LOCAL lock_timeout = '5s';

-- 1. cases ---------------------------------------------------------------------------------------------------------------------------------
CREATE TABLE service_cases (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_number        TEXT NOT NULL CHECK (case_number ~ '^[A-Z]{2,6}-[0-9]+$'),
  case_sequence      BIGINT NOT NULL CHECK (case_sequence >= 1),
  title              TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
  description        TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  customer_id        UUID,
  category_id        UUID,
  priority           TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'investigating', 'waiting', 'resolved', 'closed', 'cancelled')),
  assigned_team_id   UUID,                                    -- a team is a queue (queues are the sectors, ADR-0043)
  assigned_agent_id  UUID REFERENCES users(id),
  origin_session_id  UUID,
  resolution         TEXT CHECK (resolution IS NULL OR char_length(btrim(resolution)) BETWEEN 1 AND 4000),
  cancel_reason      TEXT CHECK (cancel_reason IS NULL OR char_length(btrim(cancel_reason)) BETWEEN 3 AND 500),
  last_reopen_reason TEXT CHECK (last_reopen_reason IS NULL OR char_length(btrim(last_reopen_reason)) BETWEEN 3 AND 500),
  created_by_user_id UUID REFERENCES users(id),
  opened_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  resolved_at        TIMESTAMPTZ,
  closed_at          TIMESTAMPTZ,
  cancelled_at       TIMESTAMPTZ,
  reopen_count       INTEGER NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
  version            INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, case_number),
  UNIQUE (tenant_id, case_sequence),
  CHECK ((status IN ('resolved', 'closed')) = (resolution IS NOT NULL)),
  CHECK ((status IN ('resolved', 'closed')) = (resolved_at IS NOT NULL)),
  CHECK ((status = 'closed') = (closed_at IS NOT NULL)),
  CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL AND cancel_reason IS NOT NULL)),
  FOREIGN KEY (tenant_id, customer_id)       REFERENCES customer_accounts (tenant_id, id),
  FOREIGN KEY (tenant_id, category_id)       REFERENCES service_categories (tenant_id, id),
  FOREIGN KEY (tenant_id, assigned_team_id)  REFERENCES queues (tenant_id, id),
  FOREIGN KEY (tenant_id, origin_session_id) REFERENCES service_sessions (tenant_id, id)
);
CREATE INDEX service_cases_status_idx   ON service_cases (tenant_id, status, opened_at DESC, id DESC);
CREATE INDEX service_cases_customer_idx ON service_cases (tenant_id, customer_id, opened_at DESC) WHERE customer_id IS NOT NULL;
CREATE INDEX service_cases_agent_idx    ON service_cases (tenant_id, assigned_agent_id, status) WHERE assigned_agent_id IS NOT NULL;
CREATE INDEX service_cases_team_idx     ON service_cases (tenant_id, assigned_team_id, status) WHERE assigned_team_id IS NOT NULL;

-- 2. which sessions refer to which case (many to many; a link is history, so it is never changed or removed) --------------------------------------
CREATE TABLE service_case_sessions (
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  case_id            UUID NOT NULL,
  session_id         UUID NOT NULL,
  relation           TEXT NOT NULL CHECK (relation IN ('origin', 'reference')),
  linked_by_user_id  UUID REFERENCES users(id),
  linked_at          TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  PRIMARY KEY (tenant_id, case_id, session_id),
  FOREIGN KEY (tenant_id, case_id)    REFERENCES service_cases (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id) REFERENCES service_sessions (tenant_id, id)
);
CREATE UNIQUE INDEX service_case_sessions_one_origin_uq ON service_case_sessions (tenant_id, case_id) WHERE relation = 'origin';
CREATE INDEX service_case_sessions_session_idx ON service_case_sessions (tenant_id, session_id);

-- 3. tasks ---------------------------------------------------------------------------------------------------------------------------------
CREATE TABLE service_tasks (
  id                   UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id            UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  title                TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200),
  description          TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 4000),
  status               TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open', 'in_progress', 'done', 'cancelled')),
  priority             TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low', 'normal', 'high', 'urgent')),
  assigned_user_id     UUID REFERENCES users(id),
  assigned_team_id     UUID,
  due_at               TIMESTAMPTZ,
  completed_at         TIMESTAMPTZ,
  completed_by_user_id UUID REFERENCES users(id),
  cancelled_at         TIMESTAMPTZ,
  cancel_reason        TEXT CHECK (cancel_reason IS NULL OR char_length(btrim(cancel_reason)) BETWEEN 3 AND 500),
  last_reopen_reason   TEXT CHECK (last_reopen_reason IS NULL OR char_length(btrim(last_reopen_reason)) BETWEEN 3 AND 500),
  -- WHO or WHAT created it. The context it belongs to is in the real foreign keys below, never in a loose (type, id) pair.
  origin_type          TEXT NOT NULL CHECK (origin_type IN ('human', 'flow', 'ai', 'api')),
  session_id           UUID,
  case_id              UUID,
  contact_id           UUID,
  flow_run_id          UUID,
  flow_seq             INTEGER,                      -- (flow_run_id, flow_seq) makes a node's effect idempotent when the engine replays it
  created_by_user_id   UUID REFERENCES users(id),
  reopen_count         INTEGER NOT NULL DEFAULT 0 CHECK (reopen_count >= 0),
  version              INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at           TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CHECK ((status = 'done') = (completed_at IS NOT NULL)),
  CHECK ((status = 'cancelled') = (cancelled_at IS NOT NULL AND cancel_reason IS NOT NULL)),
  CHECK ((origin_type = 'flow') = (flow_run_id IS NOT NULL)),
  CHECK ((flow_run_id IS NULL) = (flow_seq IS NULL)),
  FOREIGN KEY (tenant_id, assigned_team_id) REFERENCES queues (tenant_id, id),
  FOREIGN KEY (tenant_id, session_id)       REFERENCES service_sessions (tenant_id, id),
  FOREIGN KEY (tenant_id, case_id)          REFERENCES service_cases (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id)       REFERENCES contacts (tenant_id, id),
  FOREIGN KEY (tenant_id, flow_run_id)      REFERENCES flow_runs (tenant_id, id)
);
CREATE UNIQUE INDEX service_tasks_flow_effect_uq ON service_tasks (tenant_id, flow_run_id, flow_seq) WHERE flow_run_id IS NOT NULL;
CREATE INDEX service_tasks_open_idx     ON service_tasks (tenant_id, status, due_at) WHERE status IN ('open', 'in_progress');
CREATE INDEX service_tasks_assignee_idx ON service_tasks (tenant_id, assigned_user_id, status) WHERE assigned_user_id IS NOT NULL;
CREATE INDEX service_tasks_session_idx  ON service_tasks (tenant_id, session_id) WHERE session_id IS NOT NULL;
CREATE INDEX service_tasks_case_idx     ON service_tasks (tenant_id, case_id) WHERE case_id IS NOT NULL;
CREATE INDEX service_tasks_contact_idx  ON service_tasks (tenant_id, contact_id) WHERE contact_id IS NOT NULL;

-- the idempotency receipts can now refer to cases and tasks too
ALTER TABLE service_command_receipts DROP CONSTRAINT service_command_receipts_target;
ALTER TABLE service_command_receipts ADD COLUMN case_id UUID, ADD COLUMN task_id UUID;
ALTER TABLE service_command_receipts ADD FOREIGN KEY (tenant_id, case_id) REFERENCES service_cases (tenant_id, id);
ALTER TABLE service_command_receipts ADD FOREIGN KEY (tenant_id, task_id) REFERENCES service_tasks (tenant_id, id);
ALTER TABLE service_command_receipts ADD CONSTRAINT service_command_receipts_target CHECK (
  (scope = 'session.open' AND session_id IS NOT NULL AND case_id IS NULL AND task_id IS NULL)
  OR (scope = 'case.open' AND case_id IS NOT NULL AND session_id IS NULL AND task_id IS NULL)
  OR (scope = 'task.create' AND task_id IS NOT NULL AND session_id IS NULL AND case_id IS NULL));

-- the events table can now point at cases
ALTER TABLE context_events ADD FOREIGN KEY (tenant_id, case_id) REFERENCES service_cases (tenant_id, id);

-- Is this person an active member of THAT company? (SECURITY DEFINER so the answer does not depend on what the caller's own row-level rules let them read.)
CREATE FUNCTION service_active_member(p_tenant UUID, p_user UUID) RETURNS BOOLEAN AS $$
  SELECT EXISTS (SELECT 1 FROM public.memberships m WHERE m.tenant_id = p_tenant AND m.user_id = p_user AND m.status = 'active');
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION service_active_member(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION service_active_member(UUID, UUID) TO omnira_app;

-- Somebody can only be put in charge of a case or a task of a company they belong to: the foreign key to users is global, so the database checks the membership itself
-- (only when the person in charge CHANGES: a task of somebody who later left stays editable).
CREATE FUNCTION service_cases_assignee_check() RETURNS trigger AS $$
BEGIN
  IF NEW.assigned_agent_id IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.assigned_agent_id IS DISTINCT FROM OLD.assigned_agent_id)
     AND NOT public.service_active_member(NEW.tenant_id, NEW.assigned_agent_id) THEN
    RAISE EXCEPTION 'the person in charge is not an active member of this company' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;

CREATE FUNCTION service_tasks_assignee_check() RETURNS trigger AS $$
BEGIN
  IF NEW.assigned_user_id IS NOT NULL AND (TG_OP = 'INSERT' OR NEW.assigned_user_id IS DISTINCT FROM OLD.assigned_user_id)
     AND NOT public.service_active_member(NEW.tenant_id, NEW.assigned_user_id) THEN
    RAISE EXCEPTION 'the person in charge is not an active member of this company' USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;

-- 4. row guards ----------------------------------------------------------------------------------------------------------------------------
-- A case: identity immutable; version and clock advanced here; the key asked depends on WHAT changes (and a change can be several kinds at once):
--   assignment (team/agent)                          -> service.assign
--   entering or leaving resolved/closed/cancelled     -> service.close      (resolving, closing, cancelling, reopening)
--   anything else (title, text, priority, category, customer, open/investigating/waiting)  -> service.update
CREATE FUNCTION service_cases_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.case_number IS DISTINCT FROM OLD.case_number
     OR NEW.case_sequence IS DISTINCT FROM OLD.case_sequence OR NEW.opened_at IS DISTINCT FROM OLD.opened_at
     OR NEW.origin_session_id IS DISTINCT FROM OLD.origin_session_id OR NEW.created_by_user_id IS DISTINCT FROM OLD.created_by_user_id
     OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'the identity of a case never changes' USING ERRCODE = '23514';
  END IF;
  IF OLD.status IN ('closed', 'cancelled') THEN
    RAISE EXCEPTION 'a closed or cancelled case is final' USING ERRCODE = '23514';
  END IF;
  IF NEW.status <> OLD.status AND NOT (
        (OLD.status = 'open'          AND NEW.status IN ('investigating', 'waiting', 'resolved', 'cancelled'))
     OR (OLD.status = 'investigating' AND NEW.status IN ('waiting', 'resolved', 'cancelled'))
     OR (OLD.status = 'waiting'       AND NEW.status IN ('investigating', 'resolved', 'cancelled'))
     OR (OLD.status = 'resolved'      AND NEW.status IN ('closed', 'investigating'))) THEN
    RAISE EXCEPTION 'service case: % -> % is not an allowed transition', OLD.status, NEW.status USING ERRCODE = '23514';
  END IF;
  -- the KEY of what is being changed comes first, so a person without it is told so before anything else
  IF NOT public.is_system_admin() THEN
    IF (NEW.assigned_team_id IS DISTINCT FROM OLD.assigned_team_id OR NEW.assigned_agent_id IS DISTINCT FROM OLD.assigned_agent_id)
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.assign') THEN
      RAISE EXCEPTION 'service case: service.assign required' USING ERRCODE = '42501';
    END IF;
    IF (OLD.status IN ('resolved') OR NEW.status IN ('resolved', 'closed', 'cancelled')) AND NEW.status IS DISTINCT FROM OLD.status
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.close') THEN
      RAISE EXCEPTION 'service case: service.close required' USING ERRCODE = '42501';
    END IF;
    IF (NEW.title IS DISTINCT FROM OLD.title OR NEW.description IS DISTINCT FROM OLD.description OR NEW.priority IS DISTINCT FROM OLD.priority
        OR NEW.category_id IS DISTINCT FROM OLD.category_id OR NEW.customer_id IS DISTINCT FROM OLD.customer_id
        OR (NEW.status IS DISTINCT FROM OLD.status AND NEW.status IN ('open', 'investigating', 'waiting') AND OLD.status NOT IN ('resolved')))
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.update') THEN
      RAISE EXCEPTION 'service case: service.update required' USING ERRCODE = '42501';
    END IF;
  END IF;
  IF OLD.status = 'resolved' AND NEW.status = 'investigating' THEN
    IF NEW.reopen_count <> OLD.reopen_count + 1 THEN
      RAISE EXCEPTION 'reopening counts exactly once' USING ERRCODE = '23514';
    END IF;
    IF NEW.last_reopen_reason IS NULL OR char_length(btrim(NEW.last_reopen_reason)) < 3 THEN
      RAISE EXCEPTION 'reopening a case needs a reason' USING ERRCODE = '23514';
    END IF;
  ELSIF NEW.reopen_count <> OLD.reopen_count OR NEW.last_reopen_reason IS DISTINCT FROM OLD.last_reopen_reason THEN
    RAISE EXCEPTION 'the reopening count and reason only move when a resolved case is reopened' USING ERRCODE = '23514';
  END IF;
  -- the facts of a resolution, a closing and a cancellation are written WITH the status change that makes them true, never on their own
  IF NEW.status = OLD.status AND (NEW.resolution IS DISTINCT FROM OLD.resolution OR NEW.resolved_at IS DISTINCT FROM OLD.resolved_at
     OR NEW.closed_at IS DISTINCT FROM OLD.closed_at OR NEW.cancel_reason IS DISTINCT FROM OLD.cancel_reason OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at) THEN
    RAISE EXCEPTION 'resolution, closing and cancellation facts only change with the status' USING ERRCODE = '23514';
  END IF;
  IF NEW.status IN ('resolved', 'closed', 'cancelled') AND NEW.status IS DISTINCT FROM OLD.status
     AND GREATEST(COALESCE(NEW.resolved_at, '-infinity'), COALESCE(NEW.closed_at, '-infinity'), COALESCE(NEW.cancelled_at, '-infinity')) > clock_timestamp() + interval '1 minute' THEN
    RAISE EXCEPTION 'a case cannot be resolved, closed or cancelled in the future' USING ERRCODE = '23514';
  END IF;
  NEW.version := OLD.version + 1;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_cases_before_update_trg BEFORE UPDATE ON service_cases FOR EACH ROW EXECUTE FUNCTION service_cases_before_update();
CREATE TRIGGER service_cases_assignee_trg BEFORE INSERT OR UPDATE OF assigned_agent_id ON service_cases FOR EACH ROW EXECUTE FUNCTION service_cases_assignee_check();

-- A task: same discipline. Finishing or cancelling (or reopening a finished one) = service.close; assignment = service.assign; the rest = service.update.
CREATE FUNCTION service_tasks_before_update() RETURNS trigger AS $$
BEGIN
  IF NEW.id IS DISTINCT FROM OLD.id OR NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.origin_type IS DISTINCT FROM OLD.origin_type
     OR NEW.session_id IS DISTINCT FROM OLD.session_id OR NEW.case_id IS DISTINCT FROM OLD.case_id OR NEW.contact_id IS DISTINCT FROM OLD.contact_id
     OR NEW.flow_run_id IS DISTINCT FROM OLD.flow_run_id OR NEW.flow_seq IS DISTINCT FROM OLD.flow_seq
     OR NEW.created_by_user_id IS DISTINCT FROM OLD.created_by_user_id OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'the identity and the origin of a task never change' USING ERRCODE = '23514';
  END IF;
  IF OLD.status = 'cancelled' THEN
    RAISE EXCEPTION 'a cancelled task is final' USING ERRCODE = '23514';
  END IF;
  IF NEW.status <> OLD.status AND NOT (
        (OLD.status = 'open'        AND NEW.status IN ('in_progress', 'done', 'cancelled'))
     OR (OLD.status = 'in_progress' AND NEW.status IN ('open', 'done', 'cancelled'))
     OR (OLD.status = 'done'        AND NEW.status = 'open')) THEN
    RAISE EXCEPTION 'service task: % -> % is not an allowed transition', OLD.status, NEW.status USING ERRCODE = '23514';
  END IF;
  -- the KEY of what is being changed comes first, so a person without it is told so before anything else
  IF NOT public.is_system_admin() THEN
    IF (NEW.assigned_user_id IS DISTINCT FROM OLD.assigned_user_id OR NEW.assigned_team_id IS DISTINCT FROM OLD.assigned_team_id)
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.assign') THEN
      RAISE EXCEPTION 'service task: service.assign required' USING ERRCODE = '42501';
    END IF;
    IF (NEW.status IS DISTINCT FROM OLD.status AND (NEW.status IN ('done', 'cancelled') OR OLD.status = 'done'))
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.close') THEN
      RAISE EXCEPTION 'service task: service.close required' USING ERRCODE = '42501';
    END IF;
    IF (NEW.title IS DISTINCT FROM OLD.title OR NEW.description IS DISTINCT FROM OLD.description OR NEW.priority IS DISTINCT FROM OLD.priority
        OR NEW.due_at IS DISTINCT FROM OLD.due_at
        OR (NEW.status IS DISTINCT FROM OLD.status AND NEW.status IN ('open', 'in_progress') AND OLD.status <> 'done'))
       AND NOT public.actor_has_permission(NEW.tenant_id, public.current_user_id(), 'service.update') THEN
      RAISE EXCEPTION 'service task: service.update required' USING ERRCODE = '42501';
    END IF;
  END IF;
  IF OLD.status = 'done' AND NEW.status = 'done' AND (NEW.title IS DISTINCT FROM OLD.title OR NEW.description IS DISTINCT FROM OLD.description
     OR NEW.priority IS DISTINCT FROM OLD.priority OR NEW.due_at IS DISTINCT FROM OLD.due_at OR NEW.assigned_user_id IS DISTINCT FROM OLD.assigned_user_id
     OR NEW.assigned_team_id IS DISTINCT FROM OLD.assigned_team_id) THEN
    RAISE EXCEPTION 'a completed task is not edited; reopen it first' USING ERRCODE = '23514';
  END IF;
  IF OLD.status = 'done' AND NEW.status = 'open' THEN
    IF NEW.reopen_count <> OLD.reopen_count + 1 THEN
      RAISE EXCEPTION 'reopening counts exactly once' USING ERRCODE = '23514';
    END IF;
    IF NEW.last_reopen_reason IS NULL OR char_length(btrim(NEW.last_reopen_reason)) < 3 THEN
      RAISE EXCEPTION 'reopening a task needs a reason' USING ERRCODE = '23514';
    END IF;
  ELSIF NEW.reopen_count <> OLD.reopen_count OR NEW.last_reopen_reason IS DISTINCT FROM OLD.last_reopen_reason THEN
    RAISE EXCEPTION 'the reopening count and reason only move when a completed task is reopened' USING ERRCODE = '23514';
  END IF;
  IF NEW.status = OLD.status AND (NEW.completed_at IS DISTINCT FROM OLD.completed_at OR NEW.completed_by_user_id IS DISTINCT FROM OLD.completed_by_user_id
     OR NEW.cancelled_at IS DISTINCT FROM OLD.cancelled_at OR NEW.cancel_reason IS DISTINCT FROM OLD.cancel_reason) THEN
    RAISE EXCEPTION 'completion and cancellation facts only change with the status' USING ERRCODE = '23514';
  END IF;
  -- whoever completes a task signs it with their OWN id (the system session, which is not a person, is the one exception)
  IF NEW.status = 'done' AND OLD.status <> 'done' AND NOT public.is_system_admin() AND NEW.completed_by_user_id IS DISTINCT FROM public.current_user_id() THEN
    RAISE EXCEPTION 'a task is completed in the name of whoever completes it' USING ERRCODE = '23514';
  END IF;
  -- (a system session may complete a task on behalf of a person, never of somebody who does not belong to the company)
  IF NEW.status = 'done' AND OLD.status <> 'done' AND NEW.completed_by_user_id IS NOT NULL AND NOT public.service_active_member(NEW.tenant_id, NEW.completed_by_user_id) THEN
    RAISE EXCEPTION 'a task is completed by an active member of the company' USING ERRCODE = '23514';
  END IF;
  IF NEW.status IN ('done', 'cancelled') AND NEW.status IS DISTINCT FROM OLD.status
     AND GREATEST(COALESCE(NEW.completed_at, '-infinity'), COALESCE(NEW.cancelled_at, '-infinity')) > clock_timestamp() + interval '1 minute' THEN
    RAISE EXCEPTION 'a task cannot be completed or cancelled in the future' USING ERRCODE = '23514';
  END IF;
  NEW.version := OLD.version + 1;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER service_tasks_before_update_trg BEFORE UPDATE ON service_tasks FOR EACH ROW EXECUTE FUNCTION service_tasks_before_update();
CREATE TRIGGER service_tasks_assignee_trg BEFORE INSERT OR UPDATE OF assigned_user_id ON service_tasks FOR EACH ROW EXECUTE FUNCTION service_tasks_assignee_check();

-- 5. RLS -----------------------------------------------------------------------------------------------------------------------------------
-- Creating with an assignee other than yourself (or with a team) needs service.assign; creating unassigned or for yourself needs only service.create.
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['service_cases', 'service_case_sessions', 'service_tasks'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (is_system_admin() OR tenant_id IN (SELECT service_read_tenants()))', t || '_read_tenant', t);
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
  END LOOP;
END $$;
CREATE POLICY service_cases_insert_tenant ON service_cases FOR INSERT WITH CHECK (is_system_admin() OR (
  actor_has_permission(tenant_id, current_user_id(), 'service.create')
  AND status = 'open'
  AND ((assigned_agent_id IS NULL AND assigned_team_id IS NULL) OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'))));
CREATE POLICY service_cases_update_tenant ON service_cases FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close')
         OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close')
         OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'));
CREATE POLICY service_case_sessions_insert_tenant ON service_case_sessions FOR INSERT WITH CHECK (is_system_admin() OR
  actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.create'));
CREATE POLICY service_tasks_insert_tenant ON service_tasks FOR INSERT WITH CHECK (is_system_admin() OR (
  actor_has_permission(tenant_id, current_user_id(), 'service.create')
  AND status = 'open'
  AND origin_type <> 'flow'                       -- a flow's task is written by the flow engine's system session, never by a person
  AND (assigned_team_id IS NULL OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'))
  AND (assigned_user_id IS NULL OR assigned_user_id = current_user_id() OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'))));
CREATE POLICY service_tasks_update_tenant ON service_tasks FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close')
         OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'service.update') OR actor_has_permission(tenant_id, current_user_id(), 'service.close')
         OR actor_has_permission(tenant_id, current_user_id(), 'service.assign'));
-- nothing is deleted, and a case-session link is history: no UPDATE either
REVOKE DELETE ON service_cases, service_case_sessions, service_tasks FROM omnira_app;
REVOKE UPDATE ON service_case_sessions FROM omnira_app;
GRANT SELECT, INSERT, UPDATE ON service_cases, service_tasks TO omnira_app;
GRANT SELECT, INSERT ON service_case_sessions TO omnira_app;
