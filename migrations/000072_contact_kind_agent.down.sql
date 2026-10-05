UPDATE contacts SET kind = 'other' WHERE kind = 'agent';
ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('customer', 'other', 'spam'));
