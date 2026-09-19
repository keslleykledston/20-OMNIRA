-- user_identities: OIDC/external identities linked to local users
-- Global model (not tenant-scoped): issuer + subject uniquely identifies a user externally.
-- Email is an attribute, not an identity field.

CREATE TABLE IF NOT EXISTS user_identities (
  id UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  issuer TEXT NOT NULL,
  subject TEXT NOT NULL,
  email TEXT,
  display_name TEXT,
  created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
  last_login_at TIMESTAMPTZ,
  CONSTRAINT issuer_subject_unique UNIQUE (issuer, subject)
);

CREATE INDEX idx_user_identities_user_id ON user_identities(user_id);
CREATE INDEX idx_user_identities_issuer ON user_identities(issuer);
