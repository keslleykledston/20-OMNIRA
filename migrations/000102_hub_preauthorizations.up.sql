-- ADR-0039 §3.10: the Hub administrator authorizes a person BY E-MAIL before that person has an account. There is no link and
-- no token to leak: the authorization waits here and is applied the moment someone signs in with that VERIFIED address
-- (or immediately when the account already exists). It carries the access the administrator picked, per company, and it
-- expires; the administrator can revoke it any time before.
--
-- Written and read only through a system session by the Access service, which first proves in the caller's own session that
-- the caller is an active administrator of the Hub. Nobody else (not even the invited person) can read a row.
CREATE TABLE hub_preauthorizations (
  id               UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  hub_id           UUID NOT NULL REFERENCES service_hubs(id) ON DELETE CASCADE,
  email            TEXT NOT NULL CHECK (email = lower(email) AND char_length(email) BETWEEN 3 AND 254 AND position('@' IN email) > 1),
  -- [{"tenant_id": "<uuid>", "mode": "read"|"reply"}]; validated against live contracts when it is applied, never trusted from here alone
  grants           JSONB NOT NULL DEFAULT '[]' CHECK (jsonb_typeof(grants) = 'array' AND jsonb_array_length(grants) <= 50),
  status           TEXT NOT NULL DEFAULT 'pending' CHECK (status IN ('pending', 'applied', 'revoked', 'void')),
  created_by       UUID NOT NULL REFERENCES users(id),
  applied_user_id  UUID REFERENCES users(id) ON DELETE SET NULL,
  applied_at       TIMESTAMPTZ,
  expires_at       TIMESTAMPTZ NOT NULL,
  created_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at       TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK ((status = 'applied') = (applied_at IS NOT NULL))
);

-- one pending authorization per (hub, address): asking again replaces the previous one instead of stacking them
CREATE UNIQUE INDEX hub_preauthorizations_pending_uq ON hub_preauthorizations (hub_id, email) WHERE status = 'pending';
CREATE INDEX hub_preauthorizations_email_pending_idx ON hub_preauthorizations (email) WHERE status = 'pending';

ALTER TABLE hub_preauthorizations ENABLE ROW LEVEL SECURITY;
ALTER TABLE hub_preauthorizations FORCE ROW LEVEL SECURITY;

CREATE POLICY hub_preauthorizations_system ON hub_preauthorizations FOR ALL USING (is_system_admin()) WITH CHECK (is_system_admin());

-- no DELETE: the row is the trail of who authorized whom
GRANT SELECT, INSERT, UPDATE ON hub_preauthorizations TO omnira_app;
