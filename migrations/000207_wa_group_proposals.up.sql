-- ADR-0050 (M3): a ticket or a follow-up that the AI PROPOSES from a group's memory and a person approves. "Ticket" is a service case and
-- "follow-up" is a service task (ADR-0049): both exist independently of a conversation, so no phantom 1:1 conversation is created and
-- `tickets` (one active ticket per conversation) is not touched. The link group <-> created record lives here; service_* gain no column.

CREATE TABLE wa_group_proposals (
  id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id        UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id         UUID NOT NULL,
  memory_item_id   UUID,
  kind             TEXT NOT NULL CHECK (kind IN ('case','task')),
  title            TEXT NOT NULL CHECK (char_length(btrim(title)) BETWEEN 1 AND 200 AND position(E'\n' IN title) = 0),
  description      TEXT NOT NULL DEFAULT '' CHECK (char_length(description) <= 2000),
  priority         TEXT NOT NULL DEFAULT 'normal' CHECK (priority IN ('low','normal','high','urgent')),
  due_at           TIMESTAMPTZ,
  proposed_by      TEXT NOT NULL DEFAULT 'ai' CHECK (proposed_by IN ('ai','human')),
  proposed_by_user UUID REFERENCES users(id) ON DELETE SET NULL,
  status           TEXT NOT NULL DEFAULT 'pending_approval' CHECK (status IN ('pending_approval','rejected','created','failed')),
  decided_by       UUID REFERENCES users(id) ON DELETE SET NULL,
  decided_at       TIMESTAMPTZ,
  reject_reason    TEXT CHECK (reject_reason IS NULL OR char_length(btrim(reject_reason)) BETWEEN 1 AND 500),
  created_case_id  UUID,
  created_task_id  UUID,
  error_code       TEXT CHECK (error_code IS NULL OR char_length(error_code) <= 60),
  version          INTEGER NOT NULL DEFAULT 1 CHECK (version >= 1),
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CONSTRAINT wa_group_proposals_tenant_id_uq UNIQUE (tenant_id, id),
  CONSTRAINT wa_group_proposals_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  CONSTRAINT wa_group_proposals_case_fk FOREIGN KEY (tenant_id, created_case_id) REFERENCES service_cases (tenant_id, id),
  CONSTRAINT wa_group_proposals_task_fk FOREIGN KEY (tenant_id, created_task_id) REFERENCES service_tasks (tenant_id, id),
  CONSTRAINT wa_group_proposals_created_consistent CHECK (
    (status = 'created') = ((kind = 'case' AND created_case_id IS NOT NULL AND created_task_id IS NULL)
                         OR (kind = 'task' AND created_task_id IS NOT NULL AND created_case_id IS NULL))),
  CONSTRAINT wa_group_proposals_decided_consistent CHECK ((status = 'pending_approval') = (decided_at IS NULL))
);
-- an item has at most one live proposal of each kind: approving twice, or the AI proposing it again, finds the same row
CREATE UNIQUE INDEX wa_group_proposals_live_uq ON wa_group_proposals (tenant_id, memory_item_id, kind)
  WHERE memory_item_id IS NOT NULL AND status IN ('pending_approval','created');
CREATE INDEX idx_wa_group_proposals_group ON wa_group_proposals (tenant_id, group_id, status, created_at DESC);

ALTER TABLE wa_group_proposals ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_proposals FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_proposals_read_tenant ON wa_group_proposals
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_proposals_insert_tenant ON wa_group_proposals
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_proposals_update_tenant ON wa_group_proposals
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());

GRANT SELECT, INSERT, UPDATE ON wa_group_proposals TO omnira_app;
REVOKE DELETE ON wa_group_proposals FROM omnira_app; -- a decision is a record; cascades from the group/tenant run as the owner
