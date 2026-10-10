DROP POLICY IF EXISTS contact_notes_acting_one_instance ON contact_notes;
DROP POLICY IF EXISTS contact_notes_delete_delegated ON contact_notes;
DROP POLICY IF EXISTS contact_notes_update_delegated ON contact_notes;
DROP POLICY IF EXISTS contact_notes_insert_delegated ON contact_notes;
DROP POLICY IF EXISTS contact_notes_read_delegated ON contact_notes;
DELETE FROM permission_domains WHERE permission_key IN ('conversation.transfer', 'contact.note');
-- a grant or a contract ceiling that already carries these keys loses them at once (delegated_permissions only returns keys that have a domain row)
DELETE FROM role_permissions WHERE permission_key IN ('conversation.transfer', 'contact.note');
DELETE FROM permissions WHERE key IN ('conversation.transfer', 'contact.note');
