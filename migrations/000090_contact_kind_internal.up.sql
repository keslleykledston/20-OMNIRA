-- ADR-0018 addendum: "Interno" is a classification of an EXTERNAL contact declared by an operator: the team writing from a
-- personal number, partners and suppliers. It is NOT the verified staff identity (conversations.internal_user_id, a User,
-- never a contact): a declared-internal contact keeps conversation_kind = 'external_other' (contact_kind_to_conversation_kind
-- and the group rule already map every kind that is neither customer nor unclassified to "other"), so it gets no customer
-- automation (bot, automatic ticket, SLA, CSAT) but is still routed and assigned like any other conversation.
-- internal_role says which kind of internal contact it is; it exists exactly while kind = 'internal'.
ALTER TABLE contacts ADD COLUMN internal_role TEXT CHECK (internal_role IN ('team', 'partner', 'supplier'));
ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('unclassified', 'customer', 'other', 'internal', 'spam'));
ALTER TABLE contacts ADD CONSTRAINT contacts_internal_role_chk CHECK ((kind = 'internal') = (internal_role IS NOT NULL));

-- Changing only the role (supplier -> team) is also a change the open conversations' viewers must see.
CREATE OR REPLACE FUNCTION contacts_kind_realtime() RETURNS TRIGGER
LANGUAGE plpgsql AS $$
BEGIN
  IF NEW.kind IS DISTINCT FROM OLD.kind OR NEW.internal_role IS DISTINCT FROM OLD.internal_role THEN
    PERFORM realtime_emit(NEW.tenant_id, 'conversation_updated', cv.id,
      jsonb_build_object('reason', 'contact_kind_changed', 'contact_kind', NEW.kind))
    FROM conversations cv
    WHERE cv.tenant_id = NEW.tenant_id AND cv.contact_id = NEW.id AND cv.status = 'open';
  END IF;
  RETURN NEW;
END $$;
DROP TRIGGER IF EXISTS contacts_kind_realtime_trg ON contacts;
CREATE TRIGGER contacts_kind_realtime_trg AFTER UPDATE OF kind, internal_role ON contacts
  FOR EACH ROW EXECUTE FUNCTION contacts_kind_realtime();
