-- ADR-0046 H4: the durable journal of facts for external applications. Additive. A fact is written IN THE SAME TRANSACTION as the change it describes (AFTER
-- triggers on conversations, messages, contacts and tickets, so EVERY writer is covered without being touched), and only while the company has a live external
-- installation. The event carries references and counters, never a message body, a phone number or an attachment.
--
-- ORDER (Codex H0): the sequence is NOT taken inside the writer's transaction (a counter row locked until commit would deadlock with the lock order of the
-- routing engine and the contact reclassification, and would serialize every commit of a company). A fact is inserted with xid = its transaction id and
-- seq NULL. One sequencer (domain_events_sequence) numbers the facts, per company, in (xid, insertion) order, but only those whose transaction id is below
-- the horizon pg_snapshot_xmin(): every transaction that could still insert a fact BELOW that point has already finished, so no fact can ever appear under a
-- sequence number a client has already read. Readers see only numbered facts.
SET LOCAL lock_timeout = '5s';

CREATE TABLE domain_events (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),                    -- the event_id the application receives: born with the fact, stable for ever
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  ord BIGINT GENERATED ALWAYS AS IDENTITY,                           -- insertion order inside a transaction; never shown
  xid xid8 NOT NULL DEFAULT pg_current_xact_id(),                    -- the writing transaction
  seq BIGINT,                                                        -- per company, dense, assigned by the sequencer
  event_type TEXT NOT NULL CHECK (event_type IN (
    'conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1',
    'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1')),
  event_version INTEGER NOT NULL DEFAULT 1,
  aggregate_type TEXT NOT NULL CHECK (aggregate_type IN ('conversation', 'message', 'contact', 'ticket')),
  aggregate_id UUID NOT NULL,
  conversation_id UUID,
  aggregate_version BIGINT,                                          -- control_version for conversation facts: the causal order of one aggregate
  origin_kind TEXT NOT NULL CHECK (origin_kind IN ('channel', 'workspace', 'hub', 'crm', 'automation', 'system')),
  origin_installation_id UUID,
  occurred_at TIMESTAMPTZ NOT NULL DEFAULT clock_timestamp(),
  payload JSONB NOT NULL DEFAULT '{}'::jsonb CHECK (octet_length(payload::text) <= 2048)
);
CREATE UNIQUE INDEX domain_events_seq_uq ON domain_events (tenant_id, seq) WHERE seq IS NOT NULL;
CREATE INDEX domain_events_unsequenced_idx ON domain_events (xid, ord) WHERE seq IS NULL;
CREATE INDEX domain_events_retention_idx ON domain_events (tenant_id, occurred_at) WHERE seq IS NOT NULL;

CREATE TABLE domain_event_cursors (
  tenant_id UUID PRIMARY KEY REFERENCES tenants(id) ON DELETE CASCADE,
  last_seq BIGINT NOT NULL DEFAULT 0,                                -- the highest number given
  min_seq BIGINT NOT NULL DEFAULT 1,                                 -- the lowest number still kept (a cursor below min_seq - 1 must resynchronize)
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER domain_events_tenant_immutable BEFORE UPDATE OF tenant_id ON domain_events FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
CREATE TRIGGER domain_event_cursors_tenant_immutable BEFORE UPDATE OF tenant_id ON domain_event_cursors FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change();
ALTER TABLE domain_events ENABLE ROW LEVEL SECURITY;
ALTER TABLE domain_events FORCE ROW LEVEL SECURITY;
ALTER TABLE domain_event_cursors ENABLE ROW LEVEL SECURITY;
ALTER TABLE domain_event_cursors FORCE ROW LEVEL SECURITY;
-- reading: the external API (a system session, tenant predicate in every query). Nobody writes through the table: the producers, the sequencer and the
-- purge are SECURITY DEFINER functions below.
CREATE POLICY domain_events_read ON domain_events FOR SELECT USING (is_system_admin());
CREATE POLICY domain_event_cursors_read ON domain_event_cursors FOR SELECT USING (is_system_admin());
REVOKE ALL ON domain_events, domain_event_cursors FROM PUBLIC, omnira_app;
GRANT SELECT ON domain_events, domain_event_cursors TO omnira_app;

-- Is this conversation one an external application may ever hear about? The same rule as the read API (Visibility): not staff, and a contact that is not
-- internal or spam. The allow-lists of an installation are applied when it READS.
CREATE FUNCTION external_conversation_visible(p_tenant UUID, p_conversation UUID)
RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT EXISTS (
    SELECT 1 FROM conversations c
     WHERE c.tenant_id = p_tenant AND c.id = p_conversation
       AND c.internal_user_id IS NULL AND c.conversation_kind <> 'internal'
       AND EXISTS (SELECT 1 FROM contacts vc WHERE vc.tenant_id = c.tenant_id AND vc.id = c.contact_id AND vc.kind NOT IN ('internal', 'spam')))
$$;

-- The one writer of facts. It records nothing unless the company has a live installation, and nothing about a conversation the applications may not see.
CREATE FUNCTION emit_domain_event(p_tenant UUID, p_type TEXT, p_aggregate_type TEXT, p_aggregate UUID, p_conversation UUID, p_aggregate_version BIGINT,
                                  p_origin TEXT, p_origin_installation UUID, p_payload JSONB)
RETURNS VOID
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM external_app_installations WHERE tenant_id = p_tenant AND status <> 'revoked') THEN
    RETURN;
  END IF;
  IF p_conversation IS NOT NULL AND NOT external_conversation_visible(p_tenant, p_conversation) THEN
    RETURN;
  END IF;
  INSERT INTO domain_events(tenant_id, event_type, aggregate_type, aggregate_id, conversation_id, aggregate_version, origin_kind, origin_installation_id, payload)
  VALUES (p_tenant, p_type, p_aggregate_type, p_aggregate, p_conversation, p_aggregate_version, p_origin, p_origin_installation, COALESCE(p_payload, '{}'::jsonb));
END $$;

-- ------------------------------------------------------------------------------------------------------------------------------ producers
-- conversation.created.v1
CREATE FUNCTION domain_events_conversation_created() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  PERFORM emit_domain_event(NEW.tenant_id, 'conversation.created.v1', 'conversation', NEW.id, NEW.id, NEW.control_version, 'channel', NULL,
                            jsonb_build_object('status', NEW.status));
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_conversation_created_trg AFTER INSERT ON conversations FOR EACH ROW EXECUTE FUNCTION domain_events_conversation_created();

-- conversation.control_changed.v1 and conversation.closed.v1. The origin of a control change is where the NEW hold came from; a release or a drain has no new
-- hold, so it is the origin of the one that ended. The canonical service may say better with the transaction-local hint omnira.event_origin (informative).
CREATE FUNCTION domain_events_conversation_changed() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE
  hint TEXT := current_setting('omnira.event_origin', true);
  origin TEXT;
  inst UUID;
BEGIN
  IF NEW.control_version IS DISTINCT FROM OLD.control_version THEN
    origin := COALESCE(NULLIF(hint, ''), NEW.control_origin, OLD.control_origin, 'system');
    IF origin NOT IN ('workspace', 'hub', 'crm', 'automation', 'system') THEN origin := 'system'; END IF;
    inst := CASE WHEN origin = 'crm' THEN COALESCE(CASE WHEN NEW.control_kind = 'crm_application' THEN NEW.control_holder_id END,
                                                  CASE WHEN OLD.control_kind = 'crm_application' THEN OLD.control_holder_id END) END;
    PERFORM emit_domain_event(NEW.tenant_id, 'conversation.control_changed.v1', 'conversation', NEW.id, NEW.id, NEW.control_version, origin, inst,
      jsonb_build_object('control', jsonb_build_object('state', CASE WHEN NEW.control_kind IS NULL THEN 'unassigned' ELSE 'held' END, 'kind', NEW.control_kind, 'version', NEW.control_version),
                         'previous', jsonb_build_object('state', CASE WHEN OLD.control_kind IS NULL THEN 'unassigned' ELSE 'held' END, 'kind', OLD.control_kind)));
  END IF;
  IF NEW.status = 'closed' AND OLD.status <> 'closed' THEN
    origin := COALESCE(NULLIF(hint, ''), 'workspace');
    IF origin NOT IN ('workspace', 'hub', 'crm', 'automation', 'system') THEN origin := 'workspace'; END IF;
    PERFORM emit_domain_event(NEW.tenant_id, 'conversation.closed.v1', 'conversation', NEW.id, NEW.id, NEW.control_version, origin, NULL, '{}'::jsonb);
  END IF;
  RETURN NULL;
END $$;
-- Not "UPDATE OF control_version": a legacy writer (the Workspace assigning a person, the router) names assigned_to_user_id and the BEFORE guard is what moves
-- control_version, and a column list only looks at what the statement itself sets. The WHEN keeps the function from running for any other update.
CREATE TRIGGER domain_events_conversation_changed_trg AFTER UPDATE ON conversations
  FOR EACH ROW WHEN (NEW.control_version IS DISTINCT FROM OLD.control_version OR NEW.status IS DISTINCT FROM OLD.status)
  EXECUTE FUNCTION domain_events_conversation_changed();

-- message.created.v1: references and the kind only, never the text
CREATE FUNCTION domain_events_message_created() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE origin TEXT; inst UUID;
BEGIN
  IF NEW.direction = 'inbound' THEN origin := 'channel';
  ELSIF NEW.sent_by_installation_id IS NOT NULL THEN origin := 'crm'; inst := NEW.sent_by_installation_id;
  ELSIF NEW.sent_by_user_id IS NOT NULL THEN origin := 'workspace';
  ELSE origin := 'automation'; END IF;
  PERFORM emit_domain_event(NEW.tenant_id, 'message.created.v1', 'message', NEW.id, NEW.conversation_id, NULL, origin, inst,
                            jsonb_build_object('direction', NEW.direction, 'type', NEW.message_type, 'status', NEW.status));
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_message_created_trg AFTER INSERT ON messages FOR EACH ROW EXECUTE FUNCTION domain_events_message_created();

-- message.status_changed.v1: a delivery receipt is the channel talking; the sending side (queued, sent, failed, uncertain) is the platform
CREATE FUNCTION domain_events_message_status() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  PERFORM emit_domain_event(NEW.tenant_id, 'message.status_changed.v1', 'message', NEW.id, NEW.conversation_id, NULL,
                            CASE WHEN NEW.status IN ('delivered', 'read') THEN 'channel' ELSE 'system' END, NULL,
                            jsonb_build_object('status', NEW.status, 'previous', OLD.status));
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_message_status_trg AFTER UPDATE OF status ON messages
  FOR EACH ROW WHEN (NEW.status IS DISTINCT FROM OLD.status) EXECUTE FUNCTION domain_events_message_status();

-- contact.updated.v1: WHICH fields changed, never their values. A contact that becomes internal or spam is silently no longer readable.
CREATE FUNCTION domain_events_contact_updated() RETURNS TRIGGER LANGUAGE plpgsql AS $$
DECLARE changed TEXT[] := ARRAY[]::TEXT[];
BEGIN
  IF NEW.kind IN ('internal', 'spam') THEN RETURN NULL; END IF;
  IF NEW.display_name IS DISTINCT FROM OLD.display_name THEN changed := changed || 'name'::text; END IF;
  IF NEW.alias IS DISTINCT FROM OLD.alias THEN changed := changed || 'name'::text; END IF;
  IF NEW.whatsapp_name IS DISTINCT FROM OLD.whatsapp_name THEN changed := changed || 'whatsapp_name'::text; END IF;
  IF NEW.phone_e164 IS DISTINCT FROM OLD.phone_e164 THEN changed := changed || 'phone_e164'::text; END IF;
  IF NEW.kind IS DISTINCT FROM OLD.kind THEN changed := changed || 'kind'::text; END IF;
  IF cardinality(changed) = 0 THEN RETURN NULL; END IF;
  PERFORM emit_domain_event(NEW.tenant_id, 'contact.updated.v1', 'contact', NEW.id, NULL, NULL, 'workspace', NULL,
                            jsonb_build_object('fields', (SELECT jsonb_agg(DISTINCT f) FROM unnest(changed) f)));
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_contact_updated_trg AFTER UPDATE OF display_name, alias, whatsapp_name, phone_e164, kind ON contacts
  FOR EACH ROW EXECUTE FUNCTION domain_events_contact_updated();

-- ticket.linked.v1: only when the ticket has a REAL link to the ERP (ADR-0013); the ticket's content is not ours to publish
CREATE FUNCTION domain_events_ticket_linked() RETURNS TRIGGER LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.external_ticket_id IS NOT NULL AND (TG_OP = 'INSERT' OR OLD.external_ticket_id IS DISTINCT FROM NEW.external_ticket_id) THEN
    PERFORM emit_domain_event(NEW.tenant_id, 'ticket.linked.v1', 'ticket', NEW.id, NEW.conversation_id, NULL, 'system', NULL,
                              jsonb_build_object('provider', COALESCE(NEW.provider, '')));
  END IF;
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_ticket_linked_trg AFTER INSERT OR UPDATE OF external_ticket_id ON tickets
  FOR EACH ROW EXECUTE FUNCTION domain_events_ticket_linked();

-- ------------------------------------------------------------------------------------------------------------------------------ sequencer / purge
-- Numbers the facts whose transaction ids are below the horizon, per company, in (xid, ord) order. Safe to call from several workers: only one runs.
CREATE FUNCTION domain_events_sequence(p_limit INTEGER DEFAULT 500)
RETURNS INTEGER
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  numbered INTEGER := 0;
  grp RECORD;
  base BIGINT;
BEGIN
  IF p_limit < 1 OR p_limit > 5000 THEN RAISE EXCEPTION 'batch size out of range'; END IF;
  IF NOT pg_try_advisory_xact_lock(hashtextextended('omnira.domain_events.sequencer', 0)) THEN
    RETURN 0;
  END IF;
  FOR grp IN
    WITH horizon AS (SELECT pg_snapshot_xmin(pg_current_snapshot()) AS h),
         due AS (SELECT e.id, e.tenant_id, e.xid, e.ord FROM domain_events e, horizon WHERE e.seq IS NULL AND e.xid < horizon.h ORDER BY e.xid, e.ord LIMIT p_limit)
    SELECT tenant_id, array_agg(id ORDER BY xid, ord) AS ids FROM due GROUP BY tenant_id ORDER BY tenant_id
  LOOP
    INSERT INTO domain_event_cursors(tenant_id) VALUES (grp.tenant_id) ON CONFLICT (tenant_id) DO NOTHING;
    SELECT last_seq INTO base FROM domain_event_cursors WHERE tenant_id = grp.tenant_id FOR UPDATE;
    UPDATE domain_events e SET seq = base + u.n
      FROM unnest(grp.ids) WITH ORDINALITY AS u(id, n)
     WHERE e.tenant_id = grp.tenant_id AND e.id = u.id;
    UPDATE domain_event_cursors SET last_seq = base + cardinality(grp.ids), updated_at = now() WHERE tenant_id = grp.tenant_id;
    numbered := numbered + cardinality(grp.ids);
  END LOOP;
  RETURN numbered;
END $$;

-- How far behind the sequencer is (an alert when a long write transaction holds the horizon back): the age of the oldest fact still without a number.
CREATE FUNCTION domain_events_backlog(OUT waiting BIGINT, OUT oldest_seconds DOUBLE PRECISION)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$ SELECT count(*), COALESCE(EXTRACT(EPOCH FROM clock_timestamp() - min(occurred_at)), 0) FROM domain_events WHERE seq IS NULL $$;

-- Retention: drop a PREFIX of each company's numbered facts (by sequence number, so min_seq says exactly what is left) older than p_keep.
CREATE FUNCTION domain_events_purge(p_keep INTERVAL)
RETURNS BIGINT
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
DECLARE
  gone BIGINT := 0;
  t RECORD;
  cut BIGINT;
  n BIGINT;
BEGIN
  IF p_keep < interval '1 hour' THEN RAISE EXCEPTION 'retention shorter than one hour refused'; END IF;
  FOR t IN SELECT DISTINCT tenant_id FROM domain_events WHERE seq IS NOT NULL AND occurred_at < now() - p_keep LOOP
    -- numbers follow the transaction id, not the clock: a long transaction can hold a LOW number with a recent fact while a later one holds a higher number with an
    -- old fact. Only the prefix made entirely of expired facts may go: stop right before the first fact that is still inside the retention.
    SELECT COALESCE(min(seq) FILTER (WHERE occurred_at >= now() - p_keep) - 1, max(seq)) INTO cut FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL;
    CONTINUE WHEN cut IS NULL;
    UPDATE domain_event_cursors SET min_seq = cut + 1, updated_at = now() WHERE tenant_id = t.tenant_id AND min_seq < cut + 1;
    DELETE FROM domain_events WHERE tenant_id = t.tenant_id AND seq IS NOT NULL AND seq <= cut;
    GET DIAGNOSTICS n = ROW_COUNT;
    gone := gone + n;
  END LOOP;
  RETURN gone;
END $$;

REVOKE ALL ON FUNCTION external_conversation_visible(UUID, UUID), emit_domain_event(UUID, TEXT, TEXT, UUID, UUID, BIGINT, TEXT, UUID, JSONB),
  domain_events_sequence(INTEGER), domain_events_backlog(), domain_events_purge(INTERVAL) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_conversation_visible(UUID, UUID), emit_domain_event(UUID, TEXT, TEXT, UUID, UUID, BIGINT, TEXT, UUID, JSONB),
  domain_events_sequence(INTEGER), domain_events_backlog(), domain_events_purge(INTERVAL) TO omnira_app;
