-- ADR-0048 x ADR-0024: an external application (the CRM) can upload a file to send to a customer, exactly like an operator. The record of an upload names
-- its OWNER: a person (uploaded_by) or an installation (uploaded_by_installation_id), never both and never neither. Everything else about the file is unchanged.
ALTER TABLE message_outbound_media ALTER COLUMN uploaded_by DROP NOT NULL;
ALTER TABLE message_outbound_media ADD COLUMN uploaded_by_installation_id UUID;
ALTER TABLE message_outbound_media ADD CONSTRAINT message_outbound_media_installation_fk
  FOREIGN KEY (tenant_id, uploaded_by_installation_id) REFERENCES external_app_installations (tenant_id, id);
ALTER TABLE message_outbound_media ADD CONSTRAINT message_outbound_media_one_owner
  CHECK ((uploaded_by IS NULL) <> (uploaded_by_installation_id IS NULL));
CREATE INDEX message_outbound_media_installation_idx ON message_outbound_media (tenant_id, conversation_id, uploaded_by_installation_id)
  WHERE message_id IS NULL AND uploaded_by_installation_id IS NOT NULL;

-- The guard compared the owner with <>, which is NULL (so "allowed") once the owner can be NULL: compare with IS DISTINCT FROM, and freeze the installation too.
CREATE OR REPLACE FUNCTION message_outbound_media_guard() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.id <> OLD.id OR NEW.tenant_id <> OLD.tenant_id OR NEW.conversation_id <> OLD.conversation_id
     OR NEW.uploaded_by IS DISTINCT FROM OLD.uploaded_by OR NEW.uploaded_by_installation_id IS DISTINCT FROM OLD.uploaded_by_installation_id
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
