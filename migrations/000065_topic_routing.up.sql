-- ADR-0017 Wave 3: entities, routing audit, ambiguity and focus hints.
-- OMNIRA keeps WhatsApp group messages apart from conversations (ADR-0015: wa_group_messages), and group chat is exactly
-- where several subjects mix, so a routable message is EITHER a conversation message OR a group message. Group messages
-- get their own link table; everything else here references one of the two through a CHECKed pair of columns.

-- Entities recognised in a topic (generic types only; no provider-specific entity types).
CREATE TABLE topic_entities (
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id   UUID NOT NULL,
  entity_type       TEXT NOT NULL CHECK (entity_type IN ('order','invoice','contract','subscription','product','device','ticket','payment','document','service','custom')),
  canonical_key     TEXT NOT NULL CHECK (char_length(canonical_key) BETWEEN 1 AND 64),
  display_value     TEXT CHECK (display_value IS NULL OR char_length(display_value) <= 200),
  source_message_id UUID,
  confidence        NUMERIC(4,3) NOT NULL DEFAULT 1 CHECK (confidence >= 0 AND confidence <= 1),
  source            TEXT NOT NULL CHECK (source IN ('rule','ai','agent','customer','system')),
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, topic_thread_id, entity_type, canonical_key),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, source_message_id) REFERENCES messages(tenant_id, id) ON DELETE SET NULL (source_message_id)
);
CREATE INDEX topic_entities_lookup_idx ON topic_entities (tenant_id, entity_type, canonical_key);

-- Group side of the topic model.
CREATE TABLE group_message_topic_links (
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_message_id    UUID NOT NULL,
  topic_thread_id     UUID NOT NULL,
  relation            TEXT NOT NULL CHECK (relation IN ('primary','secondary','supporting','ambiguous')),
  confidence          NUMERIC(4,3) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  decision_source     TEXT NOT NULL CHECK (decision_source IN ('explicit','handoff','reply','entity','rule','ai','agent','customer','legacy')),
  routing_decision_id UUID,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, group_message_id, topic_thread_id),
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX group_message_topic_links_topic_idx ON group_message_topic_links (tenant_id, topic_thread_id, created_at);

CREATE TABLE topic_group_links (
  tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id  UUID NOT NULL,
  group_id         UUID NOT NULL,
  first_activity_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_activity_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, topic_thread_id, group_id),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, group_id) REFERENCES wa_groups(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX topic_group_links_group_idx ON topic_group_links (tenant_id, group_id);

-- Every routing decision, applied or only proposed (shadow / dry run), with the evidence that produced it.
CREATE TABLE routing_decisions (
  id                      UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id               UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id              UUID,
  group_message_id        UUID,
  status                  TEXT NOT NULL CHECK (status IN ('assigned','ambiguous','multi_topic','new_topic','unassigned')),
  selected_topic_thread_id UUID,
  decision_source         TEXT NOT NULL CHECK (decision_source IN ('explicit','handoff','reply','entity','rule','ai','agent','customer','legacy')),
  -- applied = the decision changed the data; false = proposed only (feature flag off, or AI shadow mode)
  applied                 BOOLEAN NOT NULL DEFAULT true,
  confidence              NUMERIC(4,3) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  signals                 JSONB NOT NULL DEFAULT '{}'::jsonb,
  model_provider          TEXT,
  model_name              TEXT,
  prompt_version          TEXT,
  input_tokens            INT CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens           INT CHECK (output_tokens IS NULL OR output_tokens >= 0),
  latency_ms              INT CHECK (latency_ms IS NULL OR latency_ms >= 0),
  created_at              TIMESTAMPTZ NOT NULL DEFAULT now(),
  overridden_at           TIMESTAMPTZ,
  overridden_by_user_id   UUID REFERENCES users(id) ON DELETE SET NULL,
  UNIQUE (tenant_id, id),
  CHECK ((message_id IS NOT NULL) <> (group_message_id IS NOT NULL)),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, selected_topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE SET NULL (selected_topic_thread_id)
);
-- A replay of the same event can never create a second active decision for a message.
CREATE UNIQUE INDEX routing_decisions_active_msg_uq ON routing_decisions (tenant_id, message_id) WHERE applied AND overridden_at IS NULL AND message_id IS NOT NULL;
CREATE UNIQUE INDEX routing_decisions_active_grp_uq ON routing_decisions (tenant_id, group_message_id) WHERE applied AND overridden_at IS NULL AND group_message_id IS NOT NULL;
-- One proposal per message and source while nothing was applied (dry run / shadow).
CREATE UNIQUE INDEX routing_decisions_shadow_msg_uq ON routing_decisions (tenant_id, message_id, decision_source) WHERE NOT applied AND message_id IS NOT NULL;
CREATE UNIQUE INDEX routing_decisions_shadow_grp_uq ON routing_decisions (tenant_id, group_message_id, decision_source) WHERE NOT applied AND group_message_id IS NOT NULL;
CREATE INDEX routing_decisions_topic_idx ON routing_decisions (tenant_id, selected_topic_thread_id);

-- Wave 1 left routing_decision_id on the conversation link without a constraint; add it, and give the group link one.
ALTER TABLE message_topic_links ADD FOREIGN KEY (tenant_id, routing_decision_id) REFERENCES routing_decisions(tenant_id, id) ON DELETE SET NULL (routing_decision_id);
ALTER TABLE group_message_topic_links ADD FOREIGN KEY (tenant_id, routing_decision_id) REFERENCES routing_decisions(tenant_id, id) ON DELETE SET NULL (routing_decision_id);

-- A message the router could not place with enough confidence: a person (or the customer) decides.
CREATE TABLE ambiguity_cases (
  id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  message_id               UUID,
  group_message_id         UUID,
  status                   TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved','dismissed')),
  candidate_topics         JSONB NOT NULL DEFAULT '[]'::jsonb,
  resolved_topic_thread_id UUID,
  resolution_source        TEXT CHECK (resolution_source IS NULL OR resolution_source IN ('agent','customer','rule','system')),
  resolved_by_user_id      UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at              TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  CHECK ((message_id IS NOT NULL) <> (group_message_id IS NOT NULL)),
  CHECK ((status = 'open') = (resolved_at IS NULL)),
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, group_message_id) REFERENCES wa_group_messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, resolved_topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE SET NULL (resolved_topic_thread_id)
);
CREATE UNIQUE INDEX ambiguity_cases_open_msg_uq ON ambiguity_cases (tenant_id, message_id) WHERE status = 'open' AND message_id IS NOT NULL;
CREATE UNIQUE INDEX ambiguity_cases_open_grp_uq ON ambiguity_cases (tenant_id, group_message_id) WHERE status = 'open' AND group_message_id IS NOT NULL;

-- Focus is a HINT ("this person is probably still talking about topic T"), never authority.
CREATE TABLE conversation_topic_focus (
  tenant_id              UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id        UUID,
  group_id               UUID,
  channel_participant_id UUID,
  topic_thread_id        UUID NOT NULL,
  source                 TEXT NOT NULL CHECK (source IN ('router','agent','customer','handoff')),
  confidence             NUMERIC(4,3) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  expires_at             TIMESTAMPTZ,
  updated_at             TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((conversation_id IS NOT NULL) <> (group_id IS NOT NULL)),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, group_id) REFERENCES wa_groups(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, channel_participant_id) REFERENCES channel_participants(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE
);
-- one focus per (container, participant); participant NULL = the whole conversation/group
CREATE UNIQUE INDEX conversation_topic_focus_uq ON conversation_topic_focus
  (tenant_id, COALESCE(conversation_id, group_id), COALESCE(channel_participant_id, '00000000-0000-0000-0000-000000000000'::uuid));

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['topic_entities','group_message_topic_links','topic_group_links','routing_decisions','ambiguity_cases','conversation_topic_focus'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_delete_tenant', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
