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
-- the exact same timestamp; PRIMARY KEY makes a redelivered "touch" for the same message a no-op (ON CONFLICT).
CREATE TABLE context_analysis_window_messages (
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  window_id    UUID NOT NULL,
  message_id   UUID NOT NULL,
  message_at   TIMESTAMPTZ NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  PRIMARY KEY (tenant_id, window_id, message_id),
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
