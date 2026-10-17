-- ADR-0049 gate 4 (docs/service-context/06): the facts of Service Context & Operations reach external applications through the headless journal and its webhook engine.
-- Additive. The module's own journal (context_events) stays the single source: an AFTER INSERT trigger there turns each fact into a domain_events row IN THE SAME
-- TRANSACTION, under the public name the module already documents (domain.PublicEvent), and only while a live installation of the company holds the new scope
-- `service.read`. Reading and delivering re-ask the visibility of every fact (events API and webhook worker): a fact tied to a conversation follows the conversation's
-- visibility and the installation's queue/line limits; a fact with no conversation (a case, a task, an ERP record) is shown only to an installation with NO limit.
SET LOCAL lock_timeout = '5s';

-- 1. the new scope
ALTER TABLE external_app_installations DROP CONSTRAINT external_app_installations_scopes_check;
ALTER TABLE external_app_installations ADD CONSTRAINT external_app_installations_scopes_check
  CHECK (cardinality(scopes) BETWEEN 1 AND 17
     AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                         'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send', 'service.read']::TEXT[]);

-- 2. the journal and the subscriptions know the new names
ALTER TABLE domain_events DROP CONSTRAINT domain_events_event_type_check;
ALTER TABLE domain_events ADD CONSTRAINT domain_events_event_type_check CHECK (event_type IN (
  'conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1',
  'service_session.opened.v1', 'service_session.classified.v1', 'service_session.status_changed.v1', 'service_session.closed.v1', 'service_session.reopened.v1', 'service_case.created.v1', 'service_case.updated.v1', 'service_case.resolved.v1', 'service_case.assigned.v1', 'service_case.session_linked.v1', 'service_task.created.v1', 'service_task.updated.v1', 'service_task.status_changed.v1', 'service_task.completed.v1', 'service_task.assigned.v1', 'external_record.linked.v1', 'external_record.unlinked.v1', 'external_record.status_changed.v1', 'external_request.created.v1', 'external_request.state_changed.v1'));
ALTER TABLE domain_events DROP CONSTRAINT domain_events_aggregate_type_check;
ALTER TABLE domain_events ADD CONSTRAINT domain_events_aggregate_type_check CHECK (aggregate_type IN
  ('conversation', 'message', 'contact', 'ticket', 'service_session', 'service_case', 'service_task', 'external_record', 'external_request'));
ALTER TABLE webhook_subscriptions DROP CONSTRAINT webhook_subscriptions_event_types_check;
ALTER TABLE webhook_subscriptions ADD CONSTRAINT webhook_subscriptions_event_types_check
  CHECK (cardinality(event_types) BETWEEN 1 AND 27 AND event_types <@ ARRAY[
  'conversation.created.v1', 'conversation.control_changed.v1', 'conversation.closed.v1', 'message.created.v1', 'message.status_changed.v1', 'contact.updated.v1', 'ticket.linked.v1',
  'service_session.opened.v1', 'service_session.classified.v1', 'service_session.status_changed.v1', 'service_session.closed.v1', 'service_session.reopened.v1', 'service_case.created.v1', 'service_case.updated.v1', 'service_case.resolved.v1', 'service_case.assigned.v1', 'service_case.session_linked.v1', 'service_task.created.v1', 'service_task.updated.v1', 'service_task.status_changed.v1', 'service_task.completed.v1', 'service_task.assigned.v1', 'external_record.linked.v1', 'external_record.unlinked.v1', 'external_record.status_changed.v1', 'external_request.created.v1', 'external_request.state_changed.v1']::TEXT[]);

-- 3. the producer: the module's fact -> the public name -> the journal
CREATE FUNCTION domain_events_service_context() RETURNS TRIGGER LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp AS $$
DECLARE
  to_state TEXT := NEW.payload->>'to';
  public_type TEXT;
  origin TEXT;
  links JSONB;
  body JSONB;
BEGIN
  public_type := CASE NEW.event_type
    WHEN 'service_case.opened' THEN 'service_case.created.v1'
    WHEN 'service_case.status_changed' THEN CASE WHEN to_state = 'resolved' THEN 'service_case.resolved.v1' ELSE 'service_case.updated.v1' END
    WHEN 'service_task.status_changed' THEN CASE WHEN to_state = 'done' THEN 'service_task.completed.v1' ELSE 'service_task.status_changed.v1' END
    ELSE NEW.event_type || '.v1' END;
  IF public_type NOT IN ('service_session.opened.v1', 'service_session.classified.v1', 'service_session.status_changed.v1', 'service_session.closed.v1', 'service_session.reopened.v1', 'service_case.created.v1', 'service_case.updated.v1', 'service_case.resolved.v1', 'service_case.assigned.v1', 'service_case.session_linked.v1', 'service_task.created.v1', 'service_task.updated.v1', 'service_task.status_changed.v1', 'service_task.completed.v1', 'service_task.assigned.v1', 'external_record.linked.v1', 'external_record.unlinked.v1', 'external_record.status_changed.v1', 'external_request.created.v1', 'external_request.state_changed.v1') THEN
    RETURN NULL;
  END IF;
  -- nobody who may read it, nothing journaled (emit_domain_event also requires a live installation and a visible conversation)
  IF NOT EXISTS (SELECT 1 FROM external_app_installations i WHERE i.tenant_id = NEW.tenant_id AND i.status <> 'revoked' AND 'service.read' = ANY (i.scopes)) THEN
    RETURN NULL;
  END IF;
  origin := CASE NEW.actor_type
    WHEN 'user' THEN CASE WHEN NEW.source_system = 'hub' THEN 'hub' ELSE 'workspace' END
    WHEN 'flow' THEN 'automation'
    WHEN 'ai' THEN 'automation'
    WHEN 'integration' THEN 'crm'
    ELSE 'system' END;
  links := jsonb_strip_nulls(jsonb_build_object('session_id', NEW.session_id, 'case_id', NEW.case_id, 'contact_id', NEW.contact_id, 'customer_id', NEW.customer_id));
  body := jsonb_build_object('links', links, 'actor_type', NEW.actor_type, 'data', NEW.payload);
  IF octet_length(body::text) > 2000 THEN   -- the column caps the payload at 2048 bytes: the links and the actor always fit, the module's own payload goes when it does not
    body := jsonb_build_object('links', links, 'actor_type', NEW.actor_type);
  END IF;
  PERFORM emit_domain_event(NEW.tenant_id, public_type, NEW.entity_type, NEW.entity_id, NEW.conversation_id, NULL, origin, NULL, body);
  RETURN NULL;
END $$;
CREATE TRIGGER domain_events_service_context_trg AFTER INSERT ON context_events FOR EACH ROW EXECUTE FUNCTION domain_events_service_context();

-- 4. the delivery worker re-asks, at the moment of sending, whether the installation STILL holds the scope (withdrawing it reaches the queued deliveries).
--    The table is not readable by a system session (like the allow-lists, which are also read through a function).
CREATE FUNCTION external_installation_has_scope(p_tenant UUID, p_installation UUID, p_scope TEXT)
RETURNS BOOLEAN
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT EXISTS (SELECT 1 FROM external_app_installations i
                   JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
                  WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active' AND p_scope = ANY (i.scopes))
$$;
REVOKE ALL ON FUNCTION external_installation_has_scope(UUID, UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_installation_has_scope(UUID, UUID, TEXT) TO omnira_app;
