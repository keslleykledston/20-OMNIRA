-- ADR-0015 G6: hot/cold retention of group messages. The newest days stay in Postgres; older rows are
-- exported to compressed files on the external disk by scripts/archive-wa-groups.sh, and deleted from
-- the database only after the copy is verified. A batch records one such export so a failure at any
-- step (USB gone, truncated copy) can be retried without losing or duplicating anything.
CREATE TABLE wa_group_archive_batches (
  id           UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id    UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  group_id     UUID NOT NULL,
  status       TEXT NOT NULL DEFAULT 'pending',
  row_count    INTEGER NOT NULL DEFAULT 0,
  period_start TIMESTAMPTZ,
  period_end   TIMESTAMPTZ,
  sha256       TEXT NOT NULL DEFAULT '',
  archive_path TEXT NOT NULL DEFAULT '',
  created_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
  copied_at    TIMESTAMPTZ,
  CONSTRAINT wa_group_archive_batches_tenant_id_uq UNIQUE (tenant_id, id),
  CONSTRAINT wa_group_archive_batches_group_fk FOREIGN KEY (tenant_id, group_id)
    REFERENCES wa_groups (tenant_id, id) ON DELETE CASCADE,
  -- pending: rows marked, file not yet verified on the external disk; done: archived and deleted
  -- from the database; expired: the archive file was removed (retention or an explicit purge)
  CONSTRAINT wa_group_archive_batches_status CHECK (status IN ('pending', 'done', 'expired')),
  CONSTRAINT wa_group_archive_batches_row_count CHECK (row_count >= 0)
);
CREATE INDEX idx_wa_group_archive_batches_status ON wa_group_archive_batches (status, created_at);

ALTER TABLE wa_group_messages ADD COLUMN archive_batch_id UUID;
ALTER TABLE wa_group_messages ADD CONSTRAINT wa_group_messages_archive_batch_fk
  FOREIGN KEY (tenant_id, archive_batch_id) REFERENCES wa_group_archive_batches (tenant_id, id);
CREATE INDEX idx_wa_group_messages_archive ON wa_group_messages (tenant_id, archive_batch_id) WHERE archive_batch_id IS NOT NULL;

-- "Apagar histórico" runs inside the API container, which cannot reach the external disk: it leaves
-- this request and the archive job removes the group's files on its next run.
ALTER TABLE wa_groups ADD COLUMN archive_purge_requested_at TIMESTAMPTZ;

ALTER TABLE wa_group_archive_batches ENABLE ROW LEVEL SECURITY;
ALTER TABLE wa_group_archive_batches FORCE ROW LEVEL SECURITY;
CREATE POLICY wa_group_archive_batches_read_tenant ON wa_group_archive_batches
  FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_archive_batches_insert_tenant ON wa_group_archive_batches
  FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_archive_batches_update_tenant ON wa_group_archive_batches
  FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY wa_group_archive_batches_delete_tenant ON wa_group_archive_batches
  FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON wa_group_archive_batches TO omnira_app;
