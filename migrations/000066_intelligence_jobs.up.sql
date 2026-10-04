-- ADR-0017 Wave 4: durable message-processing pipeline. AI and routing never run inside the webhook: a persisted message
-- emits an outbox event (same transaction), a consumer turns the event into a job row (idempotent), and a runner claims
-- jobs with FOR UPDATE SKIP LOCKED and a lease, so a crash (before or after the work) is recovered and a redelivery,
-- a retry or a second worker never does the work twice. The outbox is at-least-once; this table makes the processing
-- exactly-once-effective.

CREATE TABLE intelligence_jobs (
  id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id       UUID,
  group_message_id UUID,
  pipeline_version TEXT NOT NULL CHECK (char_length(pipeline_version) BETWEEN 1 AND 40),
  state            TEXT NOT NULL DEFAULT 'pending' CHECK (state IN ('pending','running','completed','failed','dead')),
  attempts         INT NOT NULL DEFAULT 0 CHECK (attempts >= 0),
  last_error_class TEXT CHECK (last_error_class IS NULL OR char_length(last_error_class) <= 60),
  next_attempt_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  locked_until     TIMESTAMPTZ,
  started_at       TIMESTAMPTZ,
  completed_at     TIMESTAMPTZ,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((message_id IS NOT NULL) <> (group_message_id IS NOT NULL)),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages(tenant_id, id) ON DELETE CASCADE
);
CREATE UNIQUE INDEX intelligence_jobs_msg_uq ON intelligence_jobs (tenant_id, message_id, pipeline_version) WHERE message_id IS NOT NULL;
CREATE UNIQUE INDEX intelligence_jobs_grp_uq ON intelligence_jobs (tenant_id, group_message_id, pipeline_version) WHERE group_message_id IS NOT NULL;
CREATE INDEX intelligence_jobs_due_idx ON intelligence_jobs (next_attempt_at) WHERE state IN ('pending','running');

ALTER TABLE intelligence_jobs ENABLE ROW LEVEL SECURITY;
ALTER TABLE intelligence_jobs FORCE ROW LEVEL SECURITY;
-- Operators may see the pipeline state (support); only the system (the worker) creates and moves jobs.
CREATE POLICY intelligence_jobs_read ON intelligence_jobs
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY intelligence_jobs_insert ON intelligence_jobs FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY intelligence_jobs_update ON intelligence_jobs FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY intelligence_jobs_delete ON intelligence_jobs FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON intelligence_jobs TO omnira_app;

-- The event. It is a job.* type because OMNIRA_JOBS (subject job.>) is the only JetStream stream the outbox publishes to.
-- The payload carries ONLY references (never the message body): the worker re-reads the content from the database,
-- tenant-safely. It is emitted in the same transaction as the message.
CREATE OR REPLACE FUNCTION inbox_message_persisted_event() RETURNS trigger AS $$
DECLARE
  evt UUID := uuid_generate_v4();
  kind TEXT;
  container UUID;
BEGIN
  IF TG_TABLE_NAME = 'messages' THEN
    kind := 'conversation';
    container := NEW.conversation_id;
  ELSE
    kind := 'group';
    container := NEW.group_id;
  END IF;
  INSERT INTO outbox_events (id, tenant_id, event_type, aggregate_type, aggregate_id, correlation_id, payload)
  VALUES (evt, NEW.tenant_id, 'job.inbox.message_persisted.v1', 'message', NEW.id::text, evt::text,
          jsonb_build_object('event_id', evt, 'tenant_id', NEW.tenant_id, 'message_id', NEW.id, 'kind', kind,
                             'container_id', container, 'occurred_at', now()));
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = public;

CREATE TRIGGER messages_persisted_event AFTER INSERT ON messages
  FOR EACH ROW WHEN (NEW.direction = 'inbound') EXECUTE FUNCTION inbox_message_persisted_event();
CREATE TRIGGER wa_group_messages_persisted_event AFTER INSERT ON wa_group_messages
  FOR EACH ROW EXECUTE FUNCTION inbox_message_persisted_event();
