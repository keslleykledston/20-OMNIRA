-- A contact has TWO names: the alias the team gave it (what everyone sees first) and the name the person declared on
-- WhatsApp (kept up to date from each message, shown smaller below). display_name stays as the EFFECTIVE name, maintained
-- by the trigger below, so every reader of display_name (lists, topics, tickets, summaries) uses the principal name.
-- Clearing the alias falls back to the WhatsApp name on its own.
ALTER TABLE contacts ADD COLUMN alias TEXT CHECK (alias IS NULL OR char_length(btrim(alias)) BETWEEN 1 AND 200);
ALTER TABLE contacts ADD COLUMN whatsapp_name TEXT NOT NULL DEFAULT '' CHECK (char_length(whatsapp_name) <= 200);

-- Every existing name came from the sender's profile (nobody could edit one before): it becomes the WhatsApp name.
-- A name equal to the phone is a placeholder, not a declared name.
UPDATE contacts SET whatsapp_name = display_name WHERE display_name <> phone_e164 AND char_length(display_name) <= 200;

CREATE FUNCTION contacts_effective_name() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  NEW.alias := NULLIF(btrim(NEW.alias), '');
  NEW.display_name := COALESCE(NEW.alias, NULLIF(btrim(NEW.whatsapp_name), ''), NEW.display_name);
  RETURN NEW;
END $$;
CREATE TRIGGER contacts_effective_name_trg BEFORE INSERT OR UPDATE OF alias, whatsapp_name ON contacts
  FOR EACH ROW EXECUTE FUNCTION contacts_effective_name();
CREATE INDEX contacts_tenant_whatsapp_name_idx ON contacts (tenant_id, lower(whatsapp_name) text_pattern_ops);
