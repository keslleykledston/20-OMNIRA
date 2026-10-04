-- ADR-0017 Wave 1: TopicThread foundation (no AI). A TopicThread is the LOGICAL subject being handled; it can span
-- conversations and channels, owns many messages (a message may belong to several topics) and relates N:N to tickets.
-- Every table is tenant-owned with FORCE RLS, and every reference to another tenant-owned row is a composite
-- (tenant_id, id) foreign key, so a UUID from another tenant can never be linked.
--
-- Conventions kept from the rest of the schema: TEXT + CHECK (no Postgres ENUMs), has_active_membership /
-- is_system_admin policies, ON DELETE CASCADE from tenants.

CREATE TABLE topic_threads (
  id                     UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  primary_contact_id     UUID,
  origin_conversation_id UUID,
  title                  TEXT NOT NULL CHECK (char_length(title) BETWEEN 1 AND 200),
  intent                 TEXT,
  category               TEXT,
  status                 TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','archived')),
  privacy_policy         TEXT NOT NULL DEFAULT 'public' CHECK (privacy_policy IN ('public','private_recommended','private_required')),
  source                 TEXT NOT NULL CHECK (source IN ('manual','rule','ai','handoff','legacy_backfill')),
  routing_confidence     NUMERIC(4,3) CHECK (routing_confidence IS NULL OR (routing_confidence >= 0 AND routing_confidence <= 1)),
  legacy_unsegmented     BOOLEAN NOT NULL DEFAULT false,
  last_activity_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_by_user_id     UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at            TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, primary_contact_id) REFERENCES contacts(tenant_id, id) ON DELETE SET NULL (primary_contact_id),
  FOREIGN KEY (tenant_id, origin_conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE SET NULL (origin_conversation_id),
  CHECK ((status = 'resolved') = (resolved_at IS NOT NULL) OR status = 'archived')
);
CREATE INDEX topic_threads_contact_idx ON topic_threads (tenant_id, primary_contact_id, status, last_activity_at DESC);
CREATE INDEX topic_threads_origin_idx ON topic_threads (tenant_id, origin_conversation_id);

-- A message may sit in several topics. routing_decision_id is filled by Wave 3 (its FK is added there).
CREATE TABLE message_topic_links (
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id          UUID NOT NULL,
  topic_thread_id     UUID NOT NULL,
  relation            TEXT NOT NULL CHECK (relation IN ('primary','secondary','supporting','ambiguous')),
  confidence          NUMERIC(4,3) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  decision_source     TEXT NOT NULL CHECK (decision_source IN ('explicit','handoff','reply','entity','rule','ai','agent','customer','legacy')),
  routing_decision_id UUID,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, message_id, topic_thread_id),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX message_topic_links_topic_idx ON message_topic_links (tenant_id, topic_thread_id, created_at);

CREATE TABLE topic_conversation_links (
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id   UUID NOT NULL,
  conversation_id   UUID NOT NULL,
  relation          TEXT NOT NULL CHECK (relation IN ('origin','active','related','handoff')),
  first_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, topic_thread_id, conversation_id),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX topic_conversation_links_conv_idx ON topic_conversation_links (tenant_id, conversation_id);

-- N:N with tickets. Existing tickets stay conversation-scoped (tickets_active_conversation_uq is untouched).
CREATE TABLE topic_ticket_links (
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id    UUID NOT NULL,
  ticket_id          UUID NOT NULL,
  relation           TEXT NOT NULL CHECK (relation IN ('primary','related','child','merged')),
  created_by         TEXT NOT NULL CHECK (created_by IN ('agent','rule','ai','system','legacy_backfill')),
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, topic_thread_id, ticket_id),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, ticket_id) REFERENCES tickets(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX topic_ticket_links_ticket_idx ON topic_ticket_links (tenant_id, ticket_id);
-- At most one PRIMARY ticket per topic; related/child/merged are unlimited.
CREATE UNIQUE INDEX topic_ticket_links_one_primary_uq ON topic_ticket_links (tenant_id, topic_thread_id) WHERE relation = 'primary';

-- Versioned summaries: a new version is always a new row (history is never overwritten).
CREATE TABLE topic_summaries (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id    UUID NOT NULL,
  version            INT NOT NULL CHECK (version >= 1),
  summary_text       TEXT NOT NULL CHECK (char_length(summary_text) <= 20000),
  structured_context JSONB NOT NULL DEFAULT '{}'::jsonb,
  status             TEXT NOT NULL CHECK (status IN ('ai_inferred','customer_confirmed','agent_confirmed','corrected','superseded')),
  source_summary_id  UUID,
  model_provider     TEXT,
  model_name         TEXT,
  prompt_version     TEXT,
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  confirmed_at       TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, topic_thread_id, version),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, source_summary_id) REFERENCES topic_summaries(tenant_id, id) ON DELETE SET NULL (source_summary_id)
);
CREATE INDEX topic_summaries_latest_idx ON topic_summaries (tenant_id, topic_thread_id, version DESC);

-- RLS: same policies as the other tenant-owned tables (members read/write under their tenant; the worker is a system session).
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['topic_threads','message_topic_links','topic_conversation_links','topic_ticket_links','topic_summaries'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_delete_tenant', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- Permissions: reading topics is part of reading the Inbox; organising them is what an agent does with their own
-- conversations. Both go to admin, supervisor and agent. (Mutations additionally require being the conversation's
-- assignee or holding conversation.manage, enforced in the handler like message sending.)
INSERT INTO permissions(key, description) VALUES
  ('topic.read',   'Read the topics (logical subjects) of conversations'),
  ('topic.manage', 'Create, edit, link, resolve and reopen topics of conversations the user operates')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor','tenant_agent')
  AND p.key IN ('topic.read','topic.manage')
ON CONFLICT DO NOTHING;
