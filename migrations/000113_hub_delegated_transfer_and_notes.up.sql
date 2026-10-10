-- ADR-0040 phase 04c: a Hub agent attending an instance may (a) hand a conversation THEY HOLD to another person of the hub or give it back to the
-- queue, and (b) read and write the notes of a contact. Two new keys, both optional per grant (no role of any company receives them):
--   conversation.transfer : hand over / give back a conversation the person holds. Data domain `conversation`, write. The write itself goes through the
--                           Hub's own write path (ADR-0037, replying.Service.Transfer), so no policy opens `conversations` for writing.
--   contact.note          : read the notes of the contacts of the instance, and add, edit and remove the person's OWN. Data domain `contact`, READ: the key only
--                           has to be delegable and let the person see the contact the note hangs from; the notes policies below ask for the key itself, and
--                           mapping it to write would hand every note holder the UPDATE/INSERT policies of the contact domain (110) at the database layer.
-- (Giving a conversation back to the queue needs only conversation.claim, like releasing one's own conversation does for a member.)
--
-- Notes are the one table here where a domain answer is too coarse: a contact reader must not read the internal notes of the company. So the
-- delegated policies of contact_notes ask for the KEY itself (actor_has_permission), not for the domain, besides inheriting the visibility of the
-- contact (and so of its conversations, queue scope included) and being pinned to the one instance of the context. Writes are restricted to the
-- person's own notes, exactly like the member's handler (only the author edits or removes a note).

INSERT INTO permissions (key, description) VALUES
  ('conversation.transfer', 'Hand over a conversation you hold, or give it back to the queue (delegated serving)'),
  ('contact.note',          'Read and write the notes of the contacts of an instance (delegated serving)')
ON CONFLICT (key) DO NOTHING;

INSERT INTO permission_domains (permission_key, domain, need) VALUES
  ('conversation.transfer', 'conversation', 'write'),
  ('contact.note',          'contact',      'read');

CREATE POLICY contact_notes_read_delegated ON contact_notes FOR SELECT
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_insert_delegated ON contact_notes FOR INSERT
  WITH CHECK (acting_hub() IS NOT NULL
              AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
              AND created_by_user_id = current_user_id()
              AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));
CREATE POLICY contact_notes_update_delegated ON contact_notes FOR UPDATE
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
         AND created_by_user_id = current_user_id()
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id))
  WITH CHECK (acting_hub() IS NOT NULL
              AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
              AND created_by_user_id = current_user_id());
CREATE POLICY contact_notes_delete_delegated ON contact_notes FOR DELETE
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'contact.note')
         AND created_by_user_id = current_user_id()
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = contact_notes.contact_id AND ct.tenant_id = contact_notes.tenant_id));

-- One instance at a time, like every other table a delegate reaches (migration 111).
CREATE POLICY contact_notes_acting_one_instance ON contact_notes AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
