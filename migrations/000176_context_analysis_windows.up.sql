-- CONTEXT.2A (Context Intelligence v2): storage and scheduling for grouped semantic analysis of a conversation's
-- messages. Strictly additive — does not touch topic_threads, topic_summaries, intelligence_jobs or any existing
-- table. Per-message routing (intelligence_jobs, migration 000066) stays fast and untouched; this is a SEPARATE,
-- asynchronous path: a window accumulates consecutive inbound messages of one conversation and is only analyzed
-- once it closes (idle/max-age/max-messages/forced trigger), never per message.
--
-- A window's membership is immutable once it leaves 'open': context_analysis_window_messages is the deterministic
-- cursor (not `created_at` alone, which collides on same-millisecond inserts or concurrent transactions) and the
-- source of `analyzed_message_ids` for CONTEXT.2B. `generation` is the fencing/ordering token per conversation —
-- a conversation accumulates windows 1, 2, 3, ... never two open at once (context_analysis_windows_open_uq).
--
-- State machine: open (accumulating) -> due (closed, waiting for a worker) -> running (leased) -> completed/dead,
-- or failed -> (retried back to due) with attempt/locked_until/last_error_class exactly like intelligence_jobs'
-- lease pattern (SKIP LOCKED, optimistic fencing by `attempt`).

CREATE TABLE context_analysis_windows (
  id                UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id         UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  conversation_id   UUID NOT NULL,
  generation        INT NOT NULL CHECK (generation >= 1),
  state             TEXT NOT NULL DEFAULT 'open' CHECK (state IN ('open','due','running','completed','failed','dead')),
  trigger           TEXT CHECK (trigger IS NULL OR trigger IN ('manual','handoff','claim','finalize','priority')),
  first_message_at  TIMESTAMPTZ NOT NULL,
  first_message_id  UUID NOT NULL,
  last_message_at   TIMESTAMPTZ NOT NULL,
  last_message_id   UUID NOT NULL,
  message_count     INT NOT NULL DEFAULT 0 CHECK (message_count >= 0),
  due_at            TIMESTAMPTZ NOT NULL,
  attempt           INT NOT NULL DEFAULT 0 CHECK (attempt >= 0),
  locked_until      TIMESTAMPTZ,
  last_error_class  TEXT CHECK (last_error_class IS NULL OR char_length(last_error_class) <= 60),
  started_at        TIMESTAMPTZ,
  completed_at      TIMESTAMPTZ,
  created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  CHECK (last_message_at >= first_message_at),
  FOREIGN KEY (tenant_id, conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, first_message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, last_message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
-- At most one accumulating window per conversation at a time.
CREATE UNIQUE INDEX context_analysis_windows_open_uq ON context_analysis_windows (tenant_id, conversation_id) WHERE state = 'open';
CREATE UNIQUE INDEX context_analysis_windows_generation_uq ON context_analysis_windows (tenant_id, conversation_id, generation);
-- The sweep (open -> due by idle/max-age) and the claim (due/expired-running -> running) scan by due_at only for
-- the states that matter, so neither index carries the bulk of completed/dead history.
CREATE INDEX context_analysis_windows_sweep_idx ON context_analysis_windows (due_at) WHERE state = 'open';
CREATE INDEX context_analysis_windows_claim_idx ON context_analysis_windows (due_at) WHERE state IN ('due');
CREATE INDEX context_analysis_windows_claim_running_idx ON context_analysis_windows (locked_until) WHERE state = 'running';
CREATE INDEX context_analysis_windows_conversation_idx ON context_analysis_windows (tenant_id, conversation_id, generation DESC);

-- The window's membership and deterministic cursor. (message_at, message_id) orders messages even when two share
-- the exact same timestamp. PRIMARY KEY is (tenant_id, message_id), not (tenant_id, window_id, message_id): a
-- message belongs to at most one window ever, so a redelivered "touch" for a message whose window already closed
-- must be rejected (23505) rather than silently re-inserted under a different, newer window_id — which would
-- double-count and double-analyze that message. A same-window redelivery is still a no-op via ON CONFLICT.
CREATE TABLE context_analysis_window_messages (
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  window_id    UUID NOT NULL,
  message_id   UUID NOT NULL,
  message_at   TIMESTAMPTZ NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, message_id),
  FOREIGN KEY (tenant_id, window_id) REFERENCES context_analysis_windows(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX context_analysis_window_messages_window_idx ON context_analysis_window_messages (tenant_id, window_id, message_at, message_id);

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['context_analysis_windows','context_analysis_window_messages'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    -- Same split as intelligence_jobs: a tenant member may SEE the scheduling state (support/debugging), but only
    -- the worker (system session) ever creates or moves a window — no human writes these rows directly.
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (is_system_admin())', t || '_insert_system', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (is_system_admin()) WITH CHECK (is_system_admin())', t || '_update_system', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (is_system_admin())', t || '_delete_system', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- Hub-delegated reads (ADR-0040): CONTEXT.2C's IsConversationFullyAnalyzed (migration 000177) joins THIS table to
-- answer staleness, and without a delegated policy here it always finds zero rows for a delegated actor — who
-- then reads as permanently "stale" no matter how caught up the conversation actually is, even though the
-- analyses themselves (000177) are already correctly readable (Codex review round 5). Additive and permissive,
-- same delegated_tenants()/EXISTS idiom as 000109/000177, scoped to the conversation (context_analysis_windows)
-- or, one hop further, the message it belongs to (context_analysis_window_messages).
CREATE POLICY context_analysis_windows_read_delegated ON context_analysis_windows FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = context_analysis_windows.conversation_id AND c.tenant_id = context_analysis_windows.tenant_id));
CREATE POLICY context_analysis_window_messages_read_delegated ON context_analysis_window_messages FOR SELECT
  USING (tenant_id IN (SELECT delegated_tenants('ticket', 'read'))
         AND EXISTS (SELECT 1 FROM messages m WHERE m.id = context_analysis_window_messages.message_id AND m.tenant_id = context_analysis_window_messages.tenant_id));

-- force_context_window: the ONLY way a plain member session (ADR-0040's manual refresh, POST .../context-analysis/
-- refresh) ever moves a window — this table's UPDATE policy is otherwise system-admin only (no human writes these
-- rows directly). SECURITY DEFINER, same idiom as lock_served_tenant/actor_has_permission: runs with the function
-- owner's privileges (which bypasses this table's RLS, same as every migration-created function here), on the
-- CALLER's own already-open connection — never a second, nested platformdb.WithTenantSession/system() session.
-- Before this, PostgresContextWindowStore.Force unconditionally opened its own system session even when called
-- from INSIDE the HTTP request's own session (the refresh handler's WithTenantSession), which could stall every
-- refresh on a small pool and exhaust a larger one under concurrent refreshes (Codex review round 8). The
-- authorization boundary is unchanged — it was already, and remains, entirely the HTTP handler's own gate
-- (topic.manage + being the attendant) before Refresh/Force is ever called; this function adds no checks of its
-- own, same as the UPDATE it replaces never had any beyond is_system_admin().
CREATE OR REPLACE FUNCTION force_context_window(p_tenant_id UUID, p_conversation_id UUID, p_trigger TEXT) RETURNS BOOLEAN AS $$
DECLARE v_forced BOOLEAN;
BEGIN
  UPDATE context_analysis_windows SET state='due', due_at=now(), trigger=p_trigger, updated_at=now()
  WHERE tenant_id = p_tenant_id AND conversation_id = p_conversation_id AND state='open';
  v_forced := FOUND;
  RETURN v_forced;
END;
$$ LANGUAGE plpgsql VOLATILE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION force_context_window(UUID, UUID, TEXT) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION force_context_window(UUID, UUID, TEXT) TO omnira_app;
