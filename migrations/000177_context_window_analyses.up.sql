-- CONTEXT.2B (Context Intelligence v2): structured analysis of a closed context_analysis_window (000176). Strictly
-- additive — topic_threads stays the single subject and topic_summaries the versioned summary (ADR-0017); this adds
-- WHERE the model's individually-cited, evidence-backed facts live, which neither table has room for. A fact is
-- never evidence that an external action ran, nor a reason to auto-close anything: `possible_resolution` is read
-- and confirmed/dismissed by a person like every other kind, never applied by itself (CLAUDE.md, this ADR).
--
-- context_window_analyses: one receipt per window (UNIQUE), chained by previous_analysis_id to the SAME
-- conversation's earlier window — that is this feature's "versão anterior", not a retry of the same window (a
-- retried window keeps the SAME row until it finally settles, same lease pattern as context_analysis_windows/
-- intelligence_jobs).
CREATE TABLE context_window_analyses (
  id                        UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id                 UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id           UUID NOT NULL,
  window_id                 UUID NOT NULL,
  previous_analysis_id      UUID,
  status                    TEXT NOT NULL CHECK (status IN ('ok','empty','failed')),
  -- The window's own short narrative (contextdelta.Proposal.Summary) — a DIFFERENT granularity from
  -- topic_summaries (one window can touch several topics' facts): informational only, never a topic's summary of
  -- record. Empty when status is 'empty' or 'failed'.
  summary_text              TEXT NOT NULL DEFAULT '' CHECK (char_length(summary_text) <= 2000),
  model_provider            TEXT,
  model_name                TEXT,
  prompt_version            TEXT,
  input_tokens              INT CHECK (input_tokens IS NULL OR input_tokens >= 0),
  output_tokens             INT CHECK (output_tokens IS NULL OR output_tokens >= 0),
  latency_ms                INT CHECK (latency_ms IS NULL OR latency_ms >= 0),
  analyzed_message_count    INT NOT NULL CHECK (analyzed_message_count >= 0),
  analyzed_first_message_at TIMESTAMPTZ,
  analyzed_last_message_at  TIMESTAMPTZ,
  error_class               TEXT CHECK (error_class IS NULL OR char_length(error_class) <= 60),
  created_at                TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, window_id),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, window_id) REFERENCES context_analysis_windows(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, previous_analysis_id) REFERENCES context_window_analyses(tenant_id, id) ON DELETE SET NULL (previous_analysis_id),
  CHECK ((analyzed_first_message_at IS NULL) = (analyzed_last_message_at IS NULL))
);
CREATE INDEX context_window_analyses_conversation_idx ON context_window_analyses (tenant_id, conversation_id, created_at DESC);

-- One row per accepted, evidence-backed fact (contextdelta.Fact, after resolving its topic_alias to a real
-- topic_thread_id and validating against the server-built Scope — see internal/intelligence/contextdelta).
-- truth_level starts 'ai_inferred' (domain.TruthLevel) and only a person raises it, via status/confirmed_*; nothing
-- here is ever written back down by a later machine run (CanReplace is enforced in Go, not here).
CREATE TABLE context_proposed_facts (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  analysis_id        UUID NOT NULL,
  topic_thread_id    UUID NOT NULL,
  kind               TEXT NOT NULL CHECK (kind IN ('need','observation','commitment','suggested_action','possible_resolution','risk')),
  text               TEXT NOT NULL CHECK (char_length(text) BETWEEN 1 AND 2000),
  truth_level        TEXT NOT NULL DEFAULT 'ai_inferred' CHECK (truth_level IN ('system_verified','provider_verified','customer_confirmed','agent_confirmed','ai_inferred')),
  status             TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','confirmed','dismissed')),
  confirmed_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  confirmed_at       TIMESTAMPTZ,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, analysis_id) REFERENCES context_window_analyses(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  CHECK ((status = 'open') = (confirmed_by_user_id IS NULL AND confirmed_at IS NULL))
);
CREATE INDEX context_proposed_facts_analysis_idx ON context_proposed_facts (tenant_id, analysis_id);
CREATE INDEX context_proposed_facts_topic_idx ON context_proposed_facts (tenant_id, topic_thread_id, created_at DESC);
-- A trigger, not just RLS, blocks changing anything but status/confirmed_by_user_id/confirmed_at/updated_at:
-- a person confirming or dismissing a fact can never rewrite WHAT the model said or WHAT it cites.
CREATE FUNCTION context_proposed_facts_guard_immutable_fields() RETURNS trigger AS $$
BEGIN
  IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id OR NEW.analysis_id IS DISTINCT FROM OLD.analysis_id OR
     NEW.topic_thread_id IS DISTINCT FROM OLD.topic_thread_id OR NEW.kind IS DISTINCT FROM OLD.kind OR
     NEW.text IS DISTINCT FROM OLD.text OR NEW.truth_level IS DISTINCT FROM OLD.truth_level OR NEW.created_at IS DISTINCT FROM OLD.created_at THEN
    RAISE EXCEPTION 'context_proposed_facts: only status/confirmed_by_user_id/confirmed_at may change' USING ERRCODE = '23514';
  END IF;
  NEW.updated_at := now();
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
CREATE TRIGGER context_proposed_facts_immutable_core BEFORE UPDATE ON context_proposed_facts
  FOR EACH ROW EXECUTE FUNCTION context_proposed_facts_guard_immutable_fields();

-- Evidence: which window messages a fact is grounded on (contextdelta.Fact.Evidence, resolved from M1..Mn aliases).
CREATE TABLE context_proposed_fact_evidence (
  tenant_id  UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  fact_id    UUID NOT NULL,
  message_id UUID NOT NULL,
  PRIMARY KEY (tenant_id, fact_id, message_id),
  FOREIGN KEY (tenant_id, fact_id) REFERENCES context_proposed_facts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);

-- Hub-delegated reads (ADR-0040): CONTEXT.2C's GetContextView admits a delegated actor who holds topic.read (its
-- domain is 'ticket', migration 000108) through the live actor_has_permission() check, but that check only
-- answers whether the KEY is held — each table's own RLS still gates the ROWS. Without a delegated policy here,
-- has_active_membership() (false for a delegated session, which has no membership row) would hide every analysis,
-- fact and evidence row even though the handler already said yes (Codex review round 3). Additive and permissive
-- (OR-ed with the membership policy, never replacing it): the same delegated_tenants()/EXISTS idiom migration
-- 000109 already uses for contacts/media, scoped to the conversation (context_window_analyses) or, one hop further,
-- to the analysis/message it belongs to (facts/evidence) — never a bare tenant_id check, so a contract's queue
-- scope (already enforced on conversations) still applies transitively.
ALTER TABLE context_window_analyses ENABLE ROW LEVEL SECURITY;
ALTER TABLE context_window_analyses FORCE ROW LEVEL SECURITY;
CREATE POLICY context_window_analyses_read_tenant ON context_window_analyses FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY context_window_analyses_read_delegated ON context_window_analyses FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = context_window_analyses.conversation_id AND c.tenant_id = context_window_analyses.tenant_id));
CREATE POLICY context_window_analyses_insert_system ON context_window_analyses FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY context_window_analyses_update_system ON context_window_analyses FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin());
CREATE POLICY context_window_analyses_delete_system ON context_window_analyses FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON context_window_analyses TO omnira_app;

ALTER TABLE context_proposed_fact_evidence ENABLE ROW LEVEL SECURITY;
ALTER TABLE context_proposed_fact_evidence FORCE ROW LEVEL SECURITY;
CREATE POLICY context_proposed_fact_evidence_read_tenant ON context_proposed_fact_evidence FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY context_proposed_fact_evidence_read_delegated ON context_proposed_fact_evidence FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = context_proposed_fact_evidence.message_id AND m.tenant_id = context_proposed_fact_evidence.tenant_id));
CREATE POLICY context_proposed_fact_evidence_insert_system ON context_proposed_fact_evidence FOR INSERT WITH CHECK (is_system_admin());
CREATE POLICY context_proposed_fact_evidence_delete_system ON context_proposed_fact_evidence FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, DELETE ON context_proposed_fact_evidence TO omnira_app;

ALTER TABLE context_proposed_facts ENABLE ROW LEVEL SECURITY;
ALTER TABLE context_proposed_facts FORCE ROW LEVEL SECURITY;
CREATE POLICY context_proposed_facts_read_tenant ON context_proposed_facts FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY context_proposed_facts_read_delegated ON context_proposed_facts FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM context_window_analyses a WHERE a.id = context_proposed_facts.analysis_id AND a.tenant_id = context_proposed_facts.tenant_id));
CREATE POLICY context_proposed_facts_insert_system ON context_proposed_facts FOR INSERT WITH CHECK (is_system_admin());
-- A person confirms/dismisses their own conversations' facts (topic.manage, same permission ADR-0017 already uses
-- for organising topics); the guard trigger above stops them from changing WHAT was said or cited either way.
CREATE POLICY context_proposed_facts_update_tenant ON context_proposed_facts FOR UPDATE
  USING (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'topic.manage'))
  WITH CHECK (is_system_admin() OR actor_has_permission(tenant_id, current_user_id(), 'topic.manage'));
CREATE POLICY context_proposed_facts_delete_system ON context_proposed_facts FOR DELETE USING (is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON context_proposed_facts TO omnira_app;
