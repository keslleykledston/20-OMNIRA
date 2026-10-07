-- Lossy by nature: a declared-internal contact goes back to "other" (the subtype is dropped).
DROP TRIGGER IF EXISTS contacts_kind_realtime_trg ON contacts;
ALTER TABLE contacts DROP CONSTRAINT IF EXISTS contacts_internal_role_chk;
UPDATE contacts SET kind = 'other' WHERE kind = 'internal';
ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('unclassified', 'customer', 'other', 'spam'));
ALTER TABLE contacts DROP COLUMN IF EXISTS internal_role;
CREATE OR REPLACE FUNCTION contacts_kind_realtime() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.kind IS DISTINCT FROM OLD.kind THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', cv.id,
      jsonb_build_object('reason', 'contact_kind_changed', 'contact_kind', NEW.kind))
    FROM conversations cv
    WHERE cv.tenant_id = NEW.tenant_id AND cv.contact_id = NEW.id AND cv.status = 'open';
  END IF;
  RETURN NEW;
END $$;
CREATE TRIGGER contacts_kind_realtime_trg AFTER UPDATE OF kind ON contacts
  FOR EACH ROW EXECUTE FUNCTION contacts_kind_realtime();
