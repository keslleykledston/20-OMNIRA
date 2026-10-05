-- ADR-0018 Wave 2: contact classification (customer / other / unclassified, plus spam as a safety state) with its origin,
-- and contact_account_links (a contact belongs to 0..N organizations; "customer" needs >= 1 active link).
-- contacts.kind stays THE classification (ADR-0014). "agent" is retired: staff are Users, never contacts.

ALTER TABLE contacts DROP CONSTRAINT contacts_kind_check;
ALTER TABLE contacts ADD COLUMN classification_source TEXT
  CHECK (classification_source IN ('manual','import','trusted_crm','ticket_flow','rule','ai_suggestion_confirmed','migration','backfill'));
ALTER TABLE contacts ADD COLUMN classified_at TIMESTAMPTZ;
ALTER TABLE contacts ADD COLUMN classified_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL;

-- Backfill, never data-destructive and never promoting anybody to customer:
--  1. what an operator decided by hand keeps its kind and gets the audited actor/time as the source of truth;
--  2. "agent" contacts become "other" (source=migration): a person on the K3G team is a User, not a contact;
--  3. a default "other" that nobody ever chose becomes "unclassified" (source=migration);
--  4. "customer" without any company evidence cannot stay customer (no link exists yet): "unclassified".
WITH last_manual AS (
  SELECT DISTINCT ON (resource_id) resource_id::uuid AS contact_id, tenant_id, actor_id, created_at, metadata->>'kind_to' AS kind_to
  FROM audit_events
  WHERE action = 'contact.kind_changed' AND resource_type = 'contact'
    AND resource_id ~ '^[0-9a-fA-F-]{36}$'
  ORDER BY resource_id, created_at DESC
)
UPDATE contacts c SET classification_source = 'manual', classified_at = lm.created_at,
       classified_by_user_id = (SELECT u.id FROM users u WHERE u.id = lm.actor_id)
FROM last_manual lm
WHERE lm.tenant_id = c.tenant_id AND lm.contact_id = c.id AND lm.kind_to = c.kind AND c.kind IN ('other','spam');

UPDATE contacts SET kind = 'other', classification_source = 'migration', classified_at = now() WHERE kind = 'agent';
UPDATE contacts SET kind = 'unclassified', classification_source = 'migration', classified_at = now()
 WHERE kind = 'other' AND classification_source IS NULL;
UPDATE contacts SET kind = 'unclassified', classification_source = 'migration', classified_at = now() WHERE kind = 'customer';

ALTER TABLE contacts ADD CONSTRAINT contacts_kind_check CHECK (kind IN ('unclassified','customer','other','spam'));
ALTER TABLE contacts ALTER COLUMN kind SET DEFAULT 'unclassified';

CREATE TABLE contact_account_links (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  contact_id          UUID NOT NULL,
  account_id          UUID NOT NULL,
  relationship_type   TEXT NOT NULL DEFAULT 'other'
    CHECK (relationship_type IN ('employee','owner','technical_contact','billing_contact','administrative_contact','representative','contractor','other')),
  status              TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active','ended')),
  -- primary is a preference for the contact, NOT exclusivity: another company stays linked and usable
  is_primary          BOOLEAN NOT NULL DEFAULT false,
  source              TEXT NOT NULL CHECK (source IN ('manual','import','trusted_crm','ticket_flow','rule','ai_suggestion_confirmed','migration','backfill')),
  confidence          NUMERIC(4,3) CHECK (confidence IS NULL OR (confidence >= 0 AND confidence <= 1)),
  created_by_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
  verified_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  verified_at         TIMESTAMPTZ,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  ended_at            TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, account_id) REFERENCES customer_accounts(tenant_id, id) ON DELETE RESTRICT,
  CHECK ((status = 'ended') = (ended_at IS NOT NULL)),
  CHECK (NOT (is_primary AND status = 'ended'))
);
-- one ACTIVE link per (contact, account); one ACTIVE primary per contact
CREATE UNIQUE INDEX contact_account_links_active_uq ON contact_account_links (tenant_id, contact_id, account_id) WHERE status = 'active';
CREATE UNIQUE INDEX contact_account_links_primary_uq ON contact_account_links (tenant_id, contact_id) WHERE status = 'active' AND is_primary;
CREATE INDEX contact_account_links_account_idx ON contact_account_links (tenant_id, account_id) WHERE status = 'active';

ALTER TABLE contact_account_links ENABLE ROW LEVEL SECURITY;
ALTER TABLE contact_account_links FORCE ROW LEVEL SECURITY;
CREATE POLICY contact_account_links_read_tenant ON contact_account_links FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY contact_account_links_insert_tenant ON contact_account_links FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY contact_account_links_update_tenant ON contact_account_links FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
-- no DELETE policy: a link is ended (ended_at), never erased
GRANT SELECT, INSERT, UPDATE ON contact_account_links TO omnira_app;

-- Invariant in the database too (defence in depth): a customer contact has at least one ACTIVE account link at commit.
-- Deferred, so one transaction can reclassify and link/unlink atomically.
CREATE FUNCTION enforce_customer_has_account_link() RETURNS trigger LANGUAGE plpgsql AS $$
DECLARE v_tenant UUID; v_contact UUID;
BEGIN
  IF TG_TABLE_NAME = 'contacts' THEN
    v_tenant := NEW.tenant_id; v_contact := NEW.id;
  ELSE
    v_tenant := COALESCE(NEW.tenant_id, OLD.tenant_id); v_contact := COALESCE(NEW.contact_id, OLD.contact_id);
  END IF;
  IF EXISTS (SELECT 1 FROM contacts WHERE tenant_id = v_tenant AND id = v_contact AND kind = 'customer')
     AND NOT EXISTS (SELECT 1 FROM contact_account_links WHERE tenant_id = v_tenant AND contact_id = v_contact AND status = 'active') THEN
    RAISE EXCEPTION 'a customer contact requires at least one active account link' USING ERRCODE = '23514';
  END IF;
  RETURN NULL;
END $$;
CREATE CONSTRAINT TRIGGER contacts_customer_needs_link AFTER INSERT OR UPDATE OF kind ON contacts
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_customer_has_account_link();
CREATE CONSTRAINT TRIGGER contact_account_links_customer_needs_link AFTER UPDATE OR DELETE ON contact_account_links
  DEFERRABLE INITIALLY DEFERRED FOR EACH ROW EXECUTE FUNCTION enforce_customer_has_account_link();
