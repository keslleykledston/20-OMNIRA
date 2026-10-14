-- ADR-0046 H2: ONE typed control of the conversation. Additive. Who holds a conversation (a tenant user, a Hub agent, an external
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

-- The guard. It never discards an UPDATE (a discarded UPDATE would lose the other columns of the statement and look like success to callers
-- that ignore RowsAffected). It derives, validates and, when something slips through, raises.
--   canonical write: the statement itself changes control_kind / control_holder_id -> control_version must advance by exactly one
--   legacy human write: the statement changes assigned_to_user_id only -> the control is derived (tenant_user) and the version advances
--   legacy write over a conversation an application holds -> SQLSTATE OM001 (the writers check first; this is the net under them)
CREATE FUNCTION conversations_control_guard() RETURNS TRIGGER
LANGUAGE plpgsql
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    IF NEW.control_kind IS NULL AND NEW.assigned_to_user_id IS NOT NULL THEN
      NEW.control_kind := 'tenant_user'; NEW.control_holder_id := NEW.assigned_to_user_id; NEW.control_since := now();
      NEW.control_origin := 'workspace'; NEW.control_version := 1;
    END IF;
  ELSIF NEW.control_kind IS DISTINCT FROM OLD.control_kind OR NEW.control_holder_id IS DISTINCT FROM OLD.control_holder_id THEN
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
-- idempotency_key: the receipt of the external command, ADR-0046 §5, is the authority and keeps the two partial unique indexes of messages out of it).
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
CREATE POLICY conversation_control_events_insert ON conversation_control_events FOR INSERT
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'conversation.takeover_external'));
REVOKE ALL ON conversation_control_events FROM PUBLIC, omnira_app;
GRANT SELECT, INSERT ON conversation_control_events TO omnira_app;   -- append-only: no UPDATE, no DELETE
