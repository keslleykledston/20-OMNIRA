-- the files an application uploaded have no person to belong to, and deleting their records would orphan the files on disk and cut the link between a message and its
-- attachment: refuse to roll back while any exist (let them expire or be swept first, or decide what to do with them by hand)
DO $$ BEGIN
  IF EXISTS (SELECT 1 FROM message_outbound_media WHERE uploaded_by_installation_id IS NOT NULL) THEN
    RAISE EXCEPTION 'message_outbound_media: files uploaded by external applications exist; they must be gone before this migration is rolled back';
  END IF;
END $$;
DROP INDEX IF EXISTS message_outbound_media_installation_idx;
ALTER TABLE message_outbound_media DROP CONSTRAINT IF EXISTS message_outbound_media_one_owner;
ALTER TABLE message_outbound_media DROP CONSTRAINT IF EXISTS message_outbound_media_installation_fk;
ALTER TABLE message_outbound_media DROP COLUMN IF EXISTS uploaded_by_installation_id;
ALTER TABLE message_outbound_media ALTER COLUMN uploaded_by SET NOT NULL;
CREATE OR REPLACE FUNCTION message_outbound_media_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.conversation_id <> OLD.conversation_id OR NEW.uploaded_by <> OLD.uploaded_by
     OR NEW.kind <> OLD.kind OR NEW.mime <> OLD.mime OR NEW.size_bytes <> OLD.size_bytes OR NEW.sha256 <> OLD.sha256
     OR NEW.file_name <> OLD.file_name OR NEW.created_at <> OLD.created_at THEN
    RAISE EXCEPTION 'message_outbound_media: the recorded file cannot change' USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  IF OLD.message_id IS NOT NULL AND NEW.message_id IS DISTINCT FROM OLD.message_id THEN
    RAISE EXCEPTION 'message_outbound_media: already attached to a message' USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  IF NEW.expires_at > OLD.expires_at THEN
    RAISE EXCEPTION 'message_outbound_media: an upload cannot be extended' USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  IF NEW.file_purged_at IS DISTINCT FROM OLD.file_purged_at AND NOT is_system_admin() THEN
    RAISE EXCEPTION 'message_outbound_media: only the worker purges files' USING ERRCODE = 'integrity_constraint_violation';
  END IF;
  RETURN NEW;
END $$;
