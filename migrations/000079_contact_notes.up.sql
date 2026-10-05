-- Notes ("comentários") that document the context of a contact, written by the people who attend them. Several per contact,
-- each with its author and time. They are internal working notes: never sent to the contact, never read by an AI here.
CREATE TABLE contact_notes (
  id                 UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id          UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  contact_id         UUID NOT NULL,
  body               TEXT NOT NULL CHECK (char_length(btrim(body)) BETWEEN 1 AND 4000),
  created_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX contact_notes_contact_idx ON contact_notes (tenant_id, contact_id, created_at DESC, id DESC);
ALTER TABLE contact_notes ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact_notes FORCE ROW LEVEL SECURITY;
CREATE POLICY contact_notes_read_tenant ON contact_notes FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY contact_notes_insert_tenant ON contact_notes FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY contact_notes_update_tenant ON contact_notes FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY contact_notes_delete_tenant ON contact_notes FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE, DELETE ON contact_notes TO omnira_app;
