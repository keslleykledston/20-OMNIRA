-- ADR-0048 H2: ONE typed control of the conversation. Additive. Who holds a conversation (a tenant user, a Hub agent, an external
-- application) lives in conversations.control_* with a monotonic control_version; assigned_to_user_id stays, as the projection for HUMAN holders
-- (every existing reader keeps working). A guard trigger derives the control for the legacy writers (they never learn the new columns) and
-- FAILS LOUD if one of them tries to take a conversation an external application holds. Nothing changes for a conversation no application holds.
SET LOCAL lock_timeout = '5s';

INSERT INTO permissions(key, description) VALUES
  ('conversation.takeover_external', 'Take over, with a reason, a conversation held by an external application')
ON CONFLICT (key) DO NOTHING;
INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin', 'tenant_supervisor') AND p.key = 'conversation.takeover_external'
ON CONFLICT DO NOTHING;

ALTER TABLE conversations
  ADD COLUMN control_kind TEXT,
  ADD COLUMN control_holder_id UUID,
  ADD COLUMN control_version BIGINT NOT NULL DEFAULT 0,
  ADD COLUMN control_since TIMESTAMPTZ,
  ADD COLUMN control_origin TEXT;

-- Backfill the human holders (no trigger yet, so no event storm). A holder who is not an active member of the tenant is a Hub agent (ADR-0037).
UPDATE conversations c SET
    control_kind = CASE WHEN EXISTS (SELECT 1 FROM memberships m WHERE m.tenant_id = c.tenant_id AND m.user_id = c.assigned_to_user_id AND m.status = 'active')
                        THEN 'tenant_user' ELSE 'hub_user' END,
    control_origin = CASE WHEN EXISTS (SELECT 1 FROM memberships m WHERE m.tenant_id = c.tenant_id AND m.user_id = c.assigned_to_user_id AND m.status = 'active')
                          THEN 'workspace' ELSE 'hub' END,
    control_holder_id = c.assigned_to_user_id,
    control_since = COALESCE(c.assigned_at, c.updated_at, now()),
    control_version = 1
WHERE c.assigned_to_user_id IS NOT NULL;

ALTER TABLE conversations
  ADD CONSTRAINT conversations_control_kind_check CHECK (control_kind IN ('tenant_user', 'hub_user', 'crm_application', 'crm_agent_verified')),
  ADD CONSTRAINT conversations_control_origin_check CHECK (control_origin IN ('workspace', 'hub', 'crm', 'system')),
  -- the shape: free, or held by exactly one typed holder; a human holder IS assigned_to_user_id, an application holder never is
  ADD CONSTRAINT conversations_control_shape CHECK (
       (control_kind IS NULL AND control_holder_id IS NULL AND control_since IS NULL AND control_origin IS NULL)
    OR (control_kind IN ('tenant_user', 'hub_user') AND control_holder_id IS NOT NULL AND control_since IS NOT NULL AND control_origin IS NOT NULL
        AND assigned_to_user_id = control_holder_id)
    OR (control_kind IN ('crm_application', 'crm_agent_verified') AND control_holder_id IS NOT NULL AND control_since IS NOT NULL AND control_origin IS NOT NULL
        AND assigned_to_user_id IS NULL));

-- the legacy readers ask "is it held?" with assigned_to_user_id IS NULL; the new ones ask control_kind IS NULL. One index serves the engine's
-- "open, free, in a queue" sweeps for both.
CREATE INDEX idx_conversations_free_open ON conversations (tenant_id, queue_id) WHERE status = 'open' AND control_kind IS NULL;

-- The only door the guard uses to know an installation exists in the tenant (the installation tables have no policy for a system session).
CREATE FUNCTION external_installation_exists(p_tenant UUID, p_installation UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$ SELECT EXISTS (SELECT 1 FROM external_app_installations WHERE tenant_id = p_tenant AND id = p_installation) $$;
REVOKE ALL ON FUNCTION external_installation_exists(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_installation_exists(UUID, UUID) TO omnira_app;

-- The capability of the control service (Codex H2 HIGH): the guard below is a boundary, not only a consistency check. The holds of external applications change only
-- inside a transaction that has asked conversation_control_begin(): it succeeds for a system session (the external API, the workers) and for a person who holds
-- conversation.takeover_external (supervisors, administrators), and leaves a row keyed by (backend, transaction) in a table the application role cannot touch. An agent
-- with arbitrary SQL cannot obtain it, so it cannot release, steal or forge an application's hold. (The table owner, a superuser, is outside the model, as always.)
CREATE UNLOGGED TABLE conversation_control_caps (
  backend_pid INTEGER PRIMARY KEY,
  xid xid8 NOT NULL,
  tenant_id UUID NOT NULL
);
ALTER TABLE conversation_control_caps ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_control_caps FORCE ROW LEVEL SECURITY;
CREATE POLICY conversation_control_caps_none ON conversation_control_caps USING (false) WITH CHECK (false);   -- explicit: only the definer functions below touch this table
REVOKE ALL ON conversation_control_caps FROM PUBLIC, omnira_app;

CREATE FUNCTION conversation_control_begin(p_tenant UUID)
RETURNS VOID
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NOT (is_system_admin() OR actor_has_permission(p_tenant, current_user_id(), 'conversation.takeover_external')) THEN
    RAISE EXCEPTION 'not allowed to change the control of an external application' USING ERRCODE = 'OM004';
  END IF;
  INSERT INTO conversation_control_caps(backend_pid, xid, tenant_id) VALUES (pg_backend_pid(), pg_current_xact_id(), p_tenant)
  ON CONFLICT (backend_pid) DO UPDATE SET xid = EXCLUDED.xid, tenant_id = EXCLUDED.tenant_id;
END $$;

CREATE FUNCTION conversation_control_cap_valid(p_tenant UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT EXISTS (SELECT 1 FROM conversation_control_caps c
                  WHERE c.backend_pid = pg_backend_pid() AND c.xid = pg_current_xact_id_if_assigned() AND c.tenant_id = p_tenant)
      -- the LOGIN role decides, never current_user: inside a SECURITY DEFINER function current_user is the owner (a superuser), which would always pass
      OR COALESCE((SELECT r.rolsuper FROM pg_roles r WHERE r.rolname = session_user), false)
$$;
REVOKE ALL ON FUNCTION conversation_control_begin(UUID), conversation_control_cap_valid(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION conversation_control_begin(UUID), conversation_control_cap_valid(UUID) TO omnira_app;

-- The guard. It never discards an UPDATE (a discarded UPDATE would lose the other columns of the statement and look like success to callers
-- that ignore RowsAffected). It derives, validates and, when something slips through, raises.
--   canonical write: the statement itself changes control_kind / control_holder_id -> control_version must advance by exactly one
--   legacy human write: the statement changes assigned_to_user_id only -> the control is derived (tenant_user) and the version advances
--   legacy write over a conversation an application holds -> SQLSTATE OM001 (the writers check first; this is the net under them)
CREATE FUNCTION conversations_control_guard() RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  touched BOOLEAN;
  external_involved BOOLEAN;
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.control_kind IN ('crm_application', 'crm_agent_verified') AND NOT conversation_control_cap_valid(NEW.tenant_id) THEN
      RAISE EXCEPTION 'only the control service may give a conversation to an external application' USING ERRCODE = 'OM001';
    END IF;
    IF NEW.control_kind IS NULL AND NEW.assigned_to_user_id IS NOT NULL THEN
      NEW.control_kind := 'tenant_user'; NEW.control_holder_id := NEW.assigned_to_user_id; NEW.control_since := now();
      NEW.control_origin := 'workspace'; NEW.control_version := 1;
    END IF;
    RETURN NEW;
  END IF;
  touched := NEW.control_kind IS DISTINCT FROM OLD.control_kind OR NEW.control_holder_id IS DISTINCT FROM OLD.control_holder_id
          OR NEW.control_version IS DISTINCT FROM OLD.control_version OR NEW.control_since IS DISTINCT FROM OLD.control_since
          OR NEW.control_origin IS DISTINCT FROM OLD.control_origin;
  external_involved := OLD.control_kind IN ('crm_application', 'crm_agent_verified') OR NEW.control_kind IN ('crm_application', 'crm_agent_verified');
  -- anything that gives, changes or ends an application's hold (or moves the human assignee of a conversation it holds) needs the capability
  IF external_involved AND (touched OR NEW.assigned_to_user_id IS DISTINCT FROM OLD.assigned_to_user_id) AND NOT conversation_control_cap_valid(OLD.tenant_id) THEN
    RAISE EXCEPTION 'conversation is held by an external application' USING ERRCODE = 'OM001';
  END IF;
  IF NEW.control_kind IS DISTINCT FROM OLD.control_kind OR NEW.control_holder_id IS DISTINCT FROM OLD.control_holder_id THEN
    -- an application's hold is only ever ended (released, drained, closed) or taken over by a supervisor (tenant_user, origin workspace): neither a
    -- Hub agent nor ANOTHER application can overwrite it, even through a canonical write
    IF OLD.control_kind IN ('crm_application', 'crm_agent_verified') AND NEW.control_kind IS NOT NULL
       AND (NEW.control_kind = 'hub_user' OR (NEW.control_kind IN ('crm_application', 'crm_agent_verified') AND NEW.control_holder_id IS DISTINCT FROM OLD.control_holder_id)) THEN
      RAISE EXCEPTION 'conversation is held by an external application' USING ERRCODE = 'OM001';
    END IF;
    IF NEW.control_version <> OLD.control_version + 1 THEN
      RAISE EXCEPTION 'control_version must advance by exactly one on a change of holder' USING ERRCODE = 'OM002';
    END IF;
    IF NEW.control_kind IN ('crm_application', 'crm_agent_verified') AND NOT external_installation_exists(NEW.tenant_id, NEW.control_holder_id) THEN
      RAISE EXCEPTION 'the external application that would hold the conversation does not exist in this company' USING ERRCODE = 'OM003';
    END IF;
  ELSIF NEW.assigned_to_user_id IS DISTINCT FROM OLD.assigned_to_user_id THEN
    IF OLD.control_kind IN ('crm_application', 'crm_agent_verified') THEN
      RAISE EXCEPTION 'conversation is held by an external application' USING ERRCODE = 'OM001';
    END IF;
    IF NEW.assigned_to_user_id IS NULL THEN
      NEW.control_kind := NULL; NEW.control_holder_id := NULL; NEW.control_since := NULL; NEW.control_origin := NULL;
    ELSE
      NEW.control_kind := 'tenant_user'; NEW.control_holder_id := NEW.assigned_to_user_id; NEW.control_since := now(); NEW.control_origin := 'workspace';
    END IF;
    NEW.control_version := OLD.control_version + 1;
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER conversations_control_guard_trg BEFORE INSERT OR UPDATE ON conversations
  FOR EACH ROW EXECUTE FUNCTION conversations_control_guard();

-- Realtime (000026 fired only on assigned_to_user_id / status, so a change of an application's control would reach nobody). Additive payload.
CREATE OR REPLACE FUNCTION conversations_realtime() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', NEW.id,
      jsonb_build_object('reason', 'created', 'assigned_to_user_id', NEW.assigned_to_user_id, 'status', NEW.status,
                         'control_kind', NEW.control_kind, 'control_version', NEW.control_version));
  ELSIF NEW.assigned_to_user_id IS DISTINCT FROM OLD.assigned_to_user_id OR NEW.status IS DISTINCT FROM OLD.status
        OR NEW.control_version IS DISTINCT FROM OLD.control_version THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', NEW.id,
      jsonb_build_object('reason', 'changed', 'assigned_to_user_id', NEW.assigned_to_user_id, 'status', NEW.status,
                         'control_kind', NEW.control_kind, 'control_version', NEW.control_version));
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS conversations_realtime_trg ON conversations;
CREATE TRIGGER conversations_realtime_trg AFTER INSERT OR UPDATE OF assigned_to_user_id, status, control_version ON conversations
  FOR EACH ROW EXECUTE FUNCTION conversations_realtime();

-- Messages sent by an external application: the sender is the installation, never a users row. sent_by_user_id stays NULL (and so does the
-- idempotency_key: the receipt of the external command, ADR-0048 §5, is the authority and keeps the two partial unique indexes of messages out of it).
ALTER TABLE messages ADD COLUMN sent_by_installation_id UUID;
ALTER TABLE messages ADD CONSTRAINT messages_sent_by_installation_fk
  FOREIGN KEY (tenant_id, sent_by_installation_id) REFERENCES external_app_installations (tenant_id, id);
ALTER TABLE messages ADD CONSTRAINT messages_sender_exclusive CHECK (sent_by_installation_id IS NULL OR sent_by_user_id IS NULL);
CREATE INDEX idx_messages_installation ON messages (tenant_id, sent_by_installation_id) WHERE sent_by_installation_id IS NOT NULL;

-- Typed history of the control changes made by the canonical service (an application, a supervisor taking over, a drain). Humans going through the
-- legacy writers keep using assignment_events. Append-only. The actor is typed; an application is never a users row.
CREATE TABLE conversation_control_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id UUID NOT NULL,
  control_version BIGINT NOT NULL,
  from_kind TEXT,
  from_holder_id UUID,
  to_kind TEXT,
  to_holder_id UUID,
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('tenant_user', 'hub_user', 'crm_application', 'system')),
  actor_id UUID,
  reason TEXT NOT NULL CHECK (char_length(reason) BETWEEN 1 AND 80),
  request_id TEXT CHECK (request_id IS NULL OR char_length(request_id) <= 64),
  claimed_agent_ref TEXT CHECK (claimed_agent_ref IS NULL OR char_length(claimed_agent_ref) <= 120),  -- what the application SAYS about its operator: unverified metadata, never an identity
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, conversation_id, control_version),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_conversation_control_events_conversation ON conversation_control_events (tenant_id, conversation_id, control_version DESC);
CREATE TRIGGER conversation_control_events_tenant_immutable BEFORE UPDATE OF tenant_id ON conversation_control_events
  FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
ALTER TABLE conversation_control_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE conversation_control_events FORCE ROW LEVEL SECURITY;
CREATE POLICY conversation_control_events_read ON conversation_control_events FOR SELECT
  USING (is_system_admin() OR has_active_membership(tenant_id, current_user_id()));
REVOKE ALL ON conversation_control_events FROM PUBLIC, omnira_app;
GRANT SELECT ON conversation_control_events TO omnira_app;   -- append-only, and written ONLY by conversation_control_record (below)

-- The one writer of the history (Codex H2 MEDIUM: a supervisor could insert history that matched no real transition). It needs the capability of the control
-- service, and it records what the conversation IS now (version, holder), so a row can only describe a change that really happened in this transaction.
CREATE FUNCTION conversation_control_record(p_tenant UUID, p_conversation UUID, p_from_kind TEXT, p_from_holder UUID, p_actor_kind TEXT, p_actor_id UUID,
                                            p_reason TEXT, p_request_id TEXT, p_claimed TEXT)
RETURNS VOID
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE c RECORD;
BEGIN
  IF NOT conversation_control_cap_valid(p_tenant) THEN
    RAISE EXCEPTION 'not allowed to write the control history' USING ERRCODE = 'OM004';
  END IF;
  SELECT control_kind, control_holder_id, control_version INTO c FROM conversations WHERE tenant_id = p_tenant AND id = p_conversation;
  IF NOT FOUND THEN RAISE EXCEPTION 'conversation not found' USING ERRCODE = 'OM005'; END IF;
  INSERT INTO conversation_control_events(tenant_id, conversation_id, control_version, from_kind, from_holder_id, to_kind, to_holder_id,
                                          actor_kind, actor_id, reason, request_id, claimed_agent_ref)
  VALUES (p_tenant, p_conversation, c.control_version, p_from_kind, p_from_holder, c.control_kind, c.control_holder_id,
          p_actor_kind, p_actor_id, left(p_reason, 80), left(p_request_id, 64), left(p_claimed, 120));
END $$;
REVOKE ALL ON FUNCTION conversation_control_record(UUID, UUID, TEXT, UUID, TEXT, UUID, TEXT, TEXT, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION conversation_control_record(UUID, UUID, TEXT, UUID, TEXT, UUID, TEXT, TEXT, TEXT) TO omnira_app;

-- Codex H2 MEDIUM: the Hub's delegated spam dequeue (migration 110) treated assigned_to_user_id IS NULL as "free"; an application's hold is not free.
CREATE OR REPLACE FUNCTION delegated_dequeue_spam(p_tenant UUID, p_contact UUID) RETURNS BIGINT AS $fn$
DECLARE n BIGINT;
BEGIN
  IF public.acting_hub() IS NULL OR NOT public.actor_has_permission(p_tenant, public.current_user_id(), 'contact.classify')
     OR NOT EXISTS (SELECT 1 FROM public.conversations c
                    WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact
                      AND public.has_active_hub_access(public.current_user_id(), p_tenant, c.queue_id, public.acting_hub(), true)) THEN
    RAISE EXCEPTION 'delegated classification not permitted' USING ERRCODE = '42501';
  END IF;
  UPDATE public.conversations c SET queue_id = NULL, routing_retry_at = NULL, updated_at = now()
  WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact AND c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.control_kind IS NULL AND c.queue_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM public.contacts ct WHERE ct.tenant_id = p_tenant AND ct.id = p_contact AND ct.kind = 'spam');
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END
$fn$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
