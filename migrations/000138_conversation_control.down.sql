-- OPERATIONAL GATE: backup and the owner's approval first. Refuses while an external application still holds a conversation (drain them first:
-- suspend or revoke the installation, which releases what it holds). DATA LOSS: drops the typed control history (conversation_control_events); the
-- human holders are untouched (assigned_to_user_id is the source for them and was never replaced). Rolling back the CODE does not need this:
-- the old code ignores the columns, and the guard trigger keeps them coherent for the old writers.
DO $$
BEGIN
  IF EXISTS (SELECT 1 FROM conversations WHERE control_kind IN ('crm_application', 'crm_agent_verified')) THEN
    RAISE EXCEPTION 'conversations are still held by an external application; release them before rolling this migration back';
  END IF;
END $$;
DROP INDEX IF EXISTS idx_messages_installation;
ALTER TABLE messages DROP CONSTRAINT IF EXISTS messages_sender_exclusive, DROP CONSTRAINT IF EXISTS messages_sent_by_installation_fk, DROP COLUMN IF EXISTS sent_by_installation_id;
DROP FUNCTION IF EXISTS conversation_control_record(UUID, UUID, TEXT, UUID, TEXT, UUID, TEXT, TEXT, TEXT);
DROP TABLE IF EXISTS conversation_control_events;
-- restore the realtime function and trigger of 000026
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
DROP TRIGGER IF EXISTS conversations_realtime_trg ON conversations;
CREATE TRIGGER conversations_realtime_trg AFTER INSERT OR UPDATE OF assigned_to_user_id, status ON conversations
  FOR EACH ROW EXECUTE FUNCTION conversations_realtime();
DROP TRIGGER IF EXISTS conversations_control_guard_trg ON conversations;
DROP FUNCTION IF EXISTS conversations_control_guard();
DROP FUNCTION IF EXISTS external_installation_exists(UUID, UUID);
DROP FUNCTION IF EXISTS conversation_control_begin(UUID);
DROP FUNCTION IF EXISTS conversation_control_cap_valid(UUID);
DROP TABLE IF EXISTS conversation_control_caps;
DROP INDEX IF EXISTS idx_conversations_free_open;
ALTER TABLE conversations
  DROP CONSTRAINT IF EXISTS conversations_control_shape,
  DROP CONSTRAINT IF EXISTS conversations_control_origin_check,
  DROP CONSTRAINT IF EXISTS conversations_control_kind_check,
  DROP COLUMN IF EXISTS control_origin,
  DROP COLUMN IF EXISTS control_since,
  DROP COLUMN IF EXISTS control_version,
  DROP COLUMN IF EXISTS control_holder_id,
  DROP COLUMN IF EXISTS control_kind;
DELETE FROM role_permissions WHERE permission_key = 'conversation.takeover_external';
DELETE FROM permissions WHERE key = 'conversation.takeover_external';

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
  WHERE c.tenant_id = p_tenant AND c.contact_id = p_contact AND c.status = 'open' AND c.assigned_to_user_id IS NULL AND c.queue_id IS NOT NULL
    AND EXISTS (SELECT 1 FROM public.contacts ct WHERE ct.tenant_id = p_tenant AND ct.id = p_contact AND ct.kind = 'spam');
  GET DIAGNOSTICS n = ROW_COUNT;
  RETURN n;
END
$fn$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
