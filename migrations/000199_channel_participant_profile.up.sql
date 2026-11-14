-- ADR-0015 addendum: identify who writes in a WhatsApp group (apparent name, company, function) WITHOUT
-- going through `contacts` — author_jid is often an opaque @lid, not a real phone, and contacts.phone_e164 is
-- mandatory and unique. channel_participants is already the right anchor: stable per (tenant, connection,
-- provider, external id), no phone required, already shared by groups and 1:1.
ALTER TABLE channel_participants
  ADD COLUMN alias TEXT,
  ADD COLUMN company_account_id UUID,
  ADD COLUMN company_role TEXT,
  ADD COLUMN profile_set_by UUID,
  ADD COLUMN profile_set_at TIMESTAMPTZ;

ALTER TABLE channel_participants
  ADD CONSTRAINT channel_participants_alias_check
    CHECK (alias IS NULL OR (char_length(btrim(alias)) >= 1 AND char_length(btrim(alias)) <= 200)),
  ADD CONSTRAINT channel_participants_company_role_check
    CHECK (company_role IS NULL OR company_role = ANY (ARRAY[
      'employee', 'owner', 'technical_contact', 'billing_contact', 'administrative_contact',
      'representative', 'contractor', 'other'
    ])),
  -- A função só faz sentido junto de uma empresa (mesmo padrão de contacts_internal_role_chk).
  ADD CONSTRAINT channel_participants_company_role_requires_account
    CHECK (company_role IS NULL OR company_account_id IS NOT NULL),
  ADD CONSTRAINT channel_participants_company_account_fk
    FOREIGN KEY (tenant_id, company_account_id) REFERENCES customer_accounts (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT channel_participants_profile_set_by_fkey
    FOREIGN KEY (profile_set_by) REFERENCES users (id) ON DELETE SET NULL;

CREATE INDEX idx_channel_participants_company_account
  ON channel_participants (tenant_id, company_account_id) WHERE company_account_id IS NOT NULL;
