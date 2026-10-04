-- ADR-0017 Wave 8: private handoff. An agent creates a short-lived, single-use invitation for a topic (typically one that
-- started in a group); the customer sends the opaque token to the business number in a private chat, and that private
-- conversation is bound to the topic. Only the SHA-256 of the token is stored, so a database reader can never redeem it.

CREATE TABLE topic_handoffs (
  id                       UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id                UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  topic_thread_id          UUID NOT NULL,
  source_group_id          UUID,
  token_hash               TEXT NOT NULL CHECK (token_hash ~ '^[0-9a-f]{64}$'),
  status                   TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','redeemed','revoked')),
  expires_at               TIMESTAMPTZ NOT NULL,
  created_by_user_id       UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at               TIMESTAMPTZ NOT NULL DEFAULT now(),
  redeemed_at              TIMESTAMPTZ,
  redeemed_message_id      UUID,
  redeemed_conversation_id UUID,
  revoked_at               TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  -- a token is globally unique: 256 random bits, so a collision means a bug, never a coincidence
  UNIQUE (token_hash),
  CHECK (expires_at > created_at),
  CHECK ((status = 'redeemed') = (redeemed_at IS NOT NULL AND redeemed_message_id IS NOT NULL AND redeemed_conversation_id IS NOT NULL)),
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL)),
  FOREIGN KEY (tenant_id, topic_thread_id) REFERENCES topic_threads(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, source_group_id) REFERENCES wa_groups(tenant_id, id) ON DELETE SET NULL (source_group_id),
  FOREIGN KEY (tenant_id, redeemed_message_id) REFERENCES messages(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, redeemed_conversation_id) REFERENCES conversations(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX topic_handoffs_topic_idx ON topic_handoffs (tenant_id, topic_thread_id, created_at DESC);
-- the redemption lookup: only pending rows are ever candidates
CREATE INDEX topic_handoffs_pending_idx ON topic_handoffs (token_hash) WHERE status = 'pending';

ALTER TABLE topic_handoffs ENABLE ROW LEVEL SECURITY;
ALTER TABLE topic_handoffs FORCE ROW LEVEL SECURITY;
CREATE POLICY topic_handoffs_read_tenant ON topic_handoffs FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY topic_handoffs_insert_tenant ON topic_handoffs FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY topic_handoffs_update_tenant ON topic_handoffs FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
-- no DELETE policy: an invitation is never erased, it is revoked or it expires (the audit trail stays)
GRANT SELECT, INSERT, UPDATE ON topic_handoffs TO omnira_app;
