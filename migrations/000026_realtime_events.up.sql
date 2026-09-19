-- P1b: realtime producers. Row changes emit a transactional NOTIFY (delivered only on COMMIT,
-- discarded on rollback) on channel `omnira_inbox_events`; the worker LISTENs and publishes to NATS
-- `inbox.events.<tenant>.<conversation>`, which the SSE endpoint fans out. Realtime is ephemeral,
-- so it deliberately does NOT use the durable Outbox (no table growth, no contention with jobs).
-- Events carry only references (no message body/phone): the UI refetches through the
-- tenant-authorized REST API, so a forged NOTIFY could at most trigger a harmless refetch.
--
-- tenant_id always comes from the changed row itself, never from a caller-supplied value.

CREATE OR REPLACE FUNCTION realtime_emit(p_tenant UUID, p_type TEXT, p_conversation UUID, p_data JSONB)
RETURNS VOID
LANGUAGE sql AS $$
  SELECT pg_notify('omnira_inbox_events',
    jsonb_build_object('tenant_id', p_tenant, 'conversation_id', p_conversation, 'type', p_type, 'data', p_data)::text);
$$;

CREATE OR REPLACE FUNCTION messages_realtime() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM realtime_emit(NEW.tenant_id, 'message_received', NEW.conversation_id,
      jsonb_build_object('message_id', NEW.id, 'direction', NEW.direction, 'status', NEW.status));
  ELSIF NEW.status IS DISTINCT FROM OLD.status THEN
    PERFORM realtime_emit(NEW.tenant_id, 'message_status', NEW.conversation_id,
      jsonb_build_object('message_id', NEW.id, 'status', NEW.status));
  END IF;
  RETURN NEW;
END $$;

CREATE OR REPLACE FUNCTION conversations_realtime() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  IF TG_OP = 'INSERT' THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', NEW.id,
      jsonb_build_object('reason', 'created', 'assigned_to_user_id', NEW.assigned_to_user_id, 'status', NEW.status));
  ELSIF NEW.assigned_to_user_id IS DISTINCT FROM OLD.assigned_to_user_id OR NEW.status IS DISTINCT FROM OLD.status THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', NEW.id,
      jsonb_build_object('reason', 'changed', 'assigned_to_user_id', NEW.assigned_to_user_id, 'status', NEW.status));
  END IF;
  RETURN NEW;
END $$;

CREATE TRIGGER messages_realtime_trg AFTER INSERT OR UPDATE OF status ON messages
  FOR EACH ROW EXECUTE FUNCTION messages_realtime();
CREATE TRIGGER conversations_realtime_trg AFTER INSERT OR UPDATE OF assigned_to_user_id, status ON conversations
  FOR EACH ROW EXECUTE FUNCTION conversations_realtime();
