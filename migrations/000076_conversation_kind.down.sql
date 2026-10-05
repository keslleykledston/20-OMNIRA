DROP FUNCTION IF EXISTS recompute_tenant_group_kinds(UUID);
DROP FUNCTION IF EXISTS recompute_groups_for_contact(UUID, UUID);
DROP FUNCTION IF EXISTS recompute_group_kind(UUID, UUID);
DROP FUNCTION IF EXISTS recompute_contact_conversation_kinds(UUID, UUID);
DROP FUNCTION IF EXISTS conversation_kind_from_counts(INT, INT, INT, INT);
ALTER TABLE wa_groups DROP COLUMN has_unclassified_participants, DROP COLUMN conversation_kind;
DROP FUNCTION IF EXISTS contact_kind_to_conversation_kind(TEXT);
DROP INDEX IF EXISTS conversations_kind_idx;
DROP INDEX IF EXISTS conversations_open_internal_uq;
-- internal conversations have no contact: they cannot survive the NOT NULL restore (their messages go with them)
DELETE FROM conversations WHERE internal_user_id IS NOT NULL;
ALTER TABLE conversations DROP CONSTRAINT conversations_internal_kind_chk, DROP CONSTRAINT conversations_party_chk, DROP CONSTRAINT conversations_internal_user_fk;
ALTER TABLE conversations ALTER COLUMN contact_id SET NOT NULL;
ALTER TABLE conversations DROP COLUMN internal_user_id, DROP COLUMN has_unclassified_participants, DROP COLUMN conversation_kind;
