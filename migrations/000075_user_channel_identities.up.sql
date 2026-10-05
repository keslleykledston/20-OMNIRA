-- ADR-0018 Wave 4: verified channel identities of INTERNAL users (staff), and the conflicts that appear when such an
-- identity also matches an existing external Contact. An identity is a security boundary: only a VERIFIED identity makes
-- inbound traffic "internal"; it never grants a permission (RBAC stays the only authority), is never inferred from a
-- name, a phone found in a profile or an AI, and a user's phone is never backfilled as verified.
CREATE TABLE user_channel_identities (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id             UUID NOT NULL,
  identity_type       TEXT NOT NULL CHECK (identity_type IN ('phone','email','provider_participant')),
  -- provider scope: '' for phone/email; "<provider>:<connection id>" for a provider participant id (a JID/LID means
  -- something only inside one provider connection)
  scope               TEXT NOT NULL DEFAULT '' CHECK (char_length(scope) <= 200),
  raw_value           TEXT NOT NULL CHECK (char_length(raw_value) BETWEEN 1 AND 200),
  normalized_value    TEXT NOT NULL CHECK (char_length(normalized_value) BETWEEN 1 AND 200),
  status              TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending','verified','revoked')),
  -- never 'ai': an AI can suggest, it can never verify
  verification_source TEXT CHECK (verification_source IN ('admin','provider_verified','challenge','import_verified')),
  verified_at         TIMESTAMPTZ,
  verified_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  revoked_at          TIMESTAMPTZ,
  revoked_by_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
  created_by_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  -- the user must belong to the tenant; a membership that goes away takes its identities with it
  FOREIGN KEY (tenant_id, user_id) REFERENCES memberships(tenant_id, user_id) ON DELETE CASCADE,
  CHECK (status <> 'verified' OR (verification_source IS NOT NULL AND verified_at IS NOT NULL)),
  CHECK (status <> 'pending' OR (verification_source IS NULL AND verified_at IS NULL)),
  CHECK (status <> 'revoked' OR revoked_at IS NOT NULL),
  CHECK (identity_type <> 'phone' OR (normalized_value ~ '^\+[1-9][0-9]{6,14}$' AND scope = '')),
  CHECK (identity_type <> 'email' OR (normalized_value = lower(normalized_value) AND position('@' in normalized_value) > 1 AND scope = '')),
  CHECK (identity_type <> 'provider_participant' OR scope <> '')
);
-- one VERIFIED owner per identity in a tenant: two staff members can never both "be" the same phone
CREATE UNIQUE INDEX user_channel_identities_verified_uq ON user_channel_identities (tenant_id, identity_type, scope, normalized_value) WHERE status = 'verified';
-- no duplicate live rows for the same user
CREATE UNIQUE INDEX user_channel_identities_live_uq ON user_channel_identities (tenant_id, user_id, identity_type, scope, normalized_value) WHERE status <> 'revoked';
CREATE INDEX user_channel_identities_user_idx ON user_channel_identities (tenant_id, user_id);

CREATE TABLE identity_resolution_conflicts (
  id                  UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id           UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  identity_id         UUID NOT NULL,
  contact_id          UUID NOT NULL,
  status              TEXT NOT NULL DEFAULT 'open' CHECK (status IN ('open','resolved')),
  -- confirmed_internal: the contact IS that staff member (resolver keeps treating the identity as internal);
  -- identity_revoked: the identity was wrong, so it stops being internal. The Contact is never deleted or merged.
  resolution          TEXT CHECK (resolution IN ('confirmed_internal','identity_revoked')),
  note                TEXT CHECK (note IS NULL OR char_length(note) <= 500),
  detected_at         TIMESTAMPTZ NOT NULL DEFAULT now(),
  resolved_at         TIMESTAMPTZ,
  resolved_by_user_id UUID REFERENCES users(id) ON DELETE SET NULL,
  UNIQUE (tenant_id, id),
  FOREIGN KEY (tenant_id, identity_id) REFERENCES user_channel_identities(tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, contact_id) REFERENCES contacts(tenant_id, id) ON DELETE CASCADE,
  CHECK ((status = 'open') = (resolution IS NULL AND resolved_at IS NULL))
);
CREATE UNIQUE INDEX identity_resolution_conflicts_open_uq ON identity_resolution_conflicts (tenant_id, identity_id, contact_id) WHERE status = 'open';
CREATE INDEX identity_resolution_conflicts_contact_idx ON identity_resolution_conflicts (tenant_id, contact_id) WHERE status = 'open';

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['user_channel_identities','identity_resolution_conflicts'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    -- no DELETE policy: identities are revoked, conflicts are resolved; the trail stays
    EXECUTE format('GRANT SELECT, INSERT, UPDATE ON %I TO omnira_app', t);
  END LOOP;
END $$;
