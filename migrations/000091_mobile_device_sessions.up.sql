-- ADR-0022 (MOBILE.1): credential per app installation for native clients. These tables are user-owned (no tenant column, like auth_sessions):
-- tenant authorization stays per request (membership + RLS), a token carries only the user. Token VALUES are never stored: only SHA-256
-- digests of 32 random bytes (high entropy, so no salt/pepper is needed); the plain value is shown once, in the response that issues it.

CREATE TABLE auth_devices (
  id            UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  user_id       UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  label         TEXT NOT NULL DEFAULT '' CHECK (length(label) <= 80),
  platform      TEXT NOT NULL CHECK (platform IN ('android', 'ios', 'other')),
  created_at    TIMESTAMPTZ NOT NULL DEFAULT now(),
  last_seen_at  TIMESTAMPTZ,
  revoked_at    TIMESTAMPTZ,
  revoked_reason TEXT CHECK (revoked_reason IN ('logout', 'user', 'admin', 'reuse', 'replaced', 'limit'))
);
CREATE INDEX auth_devices_user_idx ON auth_devices (user_id) WHERE revoked_at IS NULL;

-- One refresh chain per installation. The row is the lock that serializes refresh rotation against revocation.
CREATE TABLE auth_families (
  id                  UUID PRIMARY KEY DEFAULT gen_random_uuid(),
  device_id           UUID NOT NULL REFERENCES auth_devices(id) ON DELETE CASCADE,
  user_id             UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at          TIMESTAMPTZ NOT NULL DEFAULT now(),
  absolute_expires_at TIMESTAMPTZ NOT NULL,
  revoked_at          TIMESTAMPTZ,
  revoked_reason      TEXT
);
CREATE UNIQUE INDEX auth_families_device_uidx ON auth_families (device_id);

CREATE TABLE auth_refresh_tokens (
  token_hash  BYTEA PRIMARY KEY CHECK (length(token_hash) = 32),
  family_id   UUID NOT NULL REFERENCES auth_families(id) ON DELETE CASCADE,
  parent_hash BYTEA,
  created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at  TIMESTAMPTZ NOT NULL,
  used_at     TIMESTAMPTZ
);
CREATE INDEX auth_refresh_tokens_family_idx ON auth_refresh_tokens (family_id);

CREATE TABLE auth_access_tokens (
  token_hash BYTEA PRIMARY KEY CHECK (length(token_hash) = 32),
  family_id  UUID NOT NULL REFERENCES auth_families(id) ON DELETE CASCADE,
  user_id    UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL
);
CREATE INDEX auth_access_tokens_family_idx ON auth_access_tokens (family_id);
CREATE INDEX auth_access_tokens_expires_idx ON auth_access_tokens (expires_at);
