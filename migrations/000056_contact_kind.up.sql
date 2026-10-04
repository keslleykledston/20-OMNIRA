-- ADR-0014 slice 1: classify who a contact is for the business, separate from
-- contacts.status (record lifecycle). 'customer' | 'other' | 'spam'.
-- New and existing contacts start as 'other'; only contacts that already have
-- ACTIVE evidence of an external company association (migration 000052, born from
-- a successful ticket creation) are backfilled as 'customer'. Nothing is inferred
-- from text or from the number, and a human can always reclassify.
ALTER TABLE contacts ADD COLUMN kind TEXT NOT NULL DEFAULT 'other';
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('customer', 'other', 'spam'));

UPDATE contacts c SET kind = 'customer'
WHERE EXISTS (
  SELECT 1 FROM crm_contact_company_evidence e
  WHERE e.tenant_id = c.tenant_id AND e.contact_id = c.id AND e.revoked_at IS NULL
);

CREATE INDEX idx_contacts_tenant_kind ON contacts (tenant_id, kind, updated_at DESC, id DESC);

-- Reclassifying a contact changes what its open conversations show in the Inbox, so tell the
-- open conversations' viewers the same way every other conversation change does (row trigger ->
-- realtime_emit, see 000026): only references travel, never the contact's data.
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
