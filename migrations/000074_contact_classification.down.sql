DROP TRIGGER IF EXISTS contact_account_links_customer_needs_link ON contact_account_links;
DROP TRIGGER IF EXISTS contacts_customer_needs_link ON contacts;
DROP FUNCTION IF EXISTS enforce_customer_has_account_link();
DROP TABLE IF EXISTS contact_account_links;
ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
UPDATE contacts SET kind = 'other' WHERE kind = 'unclassified';
ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('customer','other','spam','agent'));
ALTER TABLE contacts ALTER COLUMN kind SET DEFAULT 'other';
ALTER TABLE contacts DROP COLUMN classified_by_user_id, DROP COLUMN classified_at, DROP COLUMN classification_source;
