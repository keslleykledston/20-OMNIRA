-- ADR-0046 H1: identity of an external application (a CRM) per tenant. Additive. Nothing here changes how members, the Hub or the
-- routing work; the external API is OFF unless OMNIRA_HEADLESS_ENABLED=true.
--
-- An application is NOT a user, NOT a membership and NOT a Hub agent. It is an installation approved by a tenant administrator, with
-- minimal scopes and an optional allow-list of queues and channel lines. Its credential is an opaque secret of which only the SHA-256
-- digest is stored (the secret has 256 bits of entropy, so a slow KDF adds nothing); it is shown once.
SET LOCAL lock_timeout = '5s';

INSERT INTO permissions(key, description) VALUES
  ('integration.read',   'See the external applications installed on the company (never their secrets)'),
  ('integration.manage', 'Install, suspend, rotate and revoke external applications and their webhooks')
ON CONFLICT (key) DO NOTHING;

-- administrators manage; supervisors only look. Not delegable to the Hub (the route is not in the delegable list).
INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND (
        (r.key = 'tenant_admin' AND p.key IN ('integration.read', 'integration.manage'))
     OR (r.key = 'tenant_supervisor' AND p.key = 'integration.read'))
ON CONFLICT DO NOTHING;

CREATE TABLE external_app_installations (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'suspended', 'revoked')),
  scopes TEXT[] NOT NULL
    CHECK (cardinality(scopes) BETWEEN 1 AND 16
       AND scopes <@ ARRAY['conversation.read', 'message.read', 'media.read', 'contact.read', 'events.read',
                           'conversation.claim', 'conversation.transfer', 'conversation.finalize', 'message.send']::TEXT[]),
  rate_limit_per_min INTEGER NOT NULL DEFAULT 600 CHECK (rate_limit_per_min BETWEEN 10 AND 6000),
  lease_seconds INTEGER CHECK (lease_seconds IS NULL OR lease_seconds BETWEEN 60 AND 86400),  -- NULL: the CRM holds until it releases (human holders never expire)
  allow_outside_hours BOOLEAN NOT NULL DEFAULT false,
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  suspended_at TIMESTAMPTZ,
  revoked_at TIMESTAMPTZ,
  UNIQUE (tenant_id, id),
  CHECK ((status = 'revoked') = (revoked_at IS NOT NULL))
);
-- a name is unique among the installations that are still alive; a revoked one frees its name
CREATE UNIQUE INDEX external_app_installations_name_uq ON external_app_installations (tenant_id, lower(name)) WHERE status <> 'revoked';
CREATE INDEX idx_external_app_installations_live ON external_app_installations (tenant_id) WHERE status <> 'revoked';

-- allow-lists (empty = every queue / every line of the tenant). Composite FKs: the database refuses another tenant's queue or line.
CREATE TABLE external_app_installation_queues (
  tenant_id UUID NOT NULL,
  installation_id UUID NOT NULL,
  queue_id UUID NOT NULL,
  PRIMARY KEY (installation_id, queue_id),
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE
);
CREATE TABLE external_app_installation_channels (
  tenant_id UUID NOT NULL,
  installation_id UUID NOT NULL,
  connection_id UUID NOT NULL,
  PRIMARY KEY (installation_id, connection_id),
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, connection_id) REFERENCES channel_connections (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE external_app_credentials (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL,
  installation_id UUID NOT NULL,
  key_id TEXT NOT NULL UNIQUE CHECK (key_id ~ '^[a-z0-9]{16}$'),         -- the public half of the token: omn_ext_<key_id>.<secret>
  secret_digest BYTEA NOT NULL CHECK (octet_length(secret_digest) = 32),   -- SHA-256 of the secret; never selectable by the application role
  label TEXT NOT NULL DEFAULT '' CHECK (char_length(label) <= 80),
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  expires_at TIMESTAMPTZ NOT NULL,
  revoked_at TIMESTAMPTZ,
  last_used_at TIMESTAMPTZ,
  FOREIGN KEY (tenant_id, installation_id) REFERENCES external_app_installations (tenant_id, id) ON DELETE CASCADE,
  CHECK (expires_at > created_at)
);
CREATE INDEX idx_external_app_credentials_installation ON external_app_credentials (tenant_id, installation_id) WHERE revoked_at IS NULL;

-- tenant_id never changes (same guard the routing tables got in 000131)
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['external_app_installations', 'external_app_installation_queues', 'external_app_installation_channels', 'external_app_credentials'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
  END LOOP;
END $$;

-- RLS. Only a person holding the permission reaches these tables. There is deliberately NO system-session branch (is_system_admin()): the
-- external API itself runs in a system session (ADR-0037 pattern), and it must not be able to read, list or change installations of any
-- tenant by forgetting a predicate. It reaches this data only through the SECURITY DEFINER functions below, each bound to a tenant,
-- an installation and a credential. A Hub agent is not a member, so actor_has_permission() is false for it.
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['external_app_installations', 'external_app_installation_queues', 'external_app_installation_channels', 'external_app_credentials'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format($p$CREATE POLICY %I ON %I FOR SELECT USING (actor_has_permission(tenant_id, current_user_id(), 'integration.read'))$p$, t || '_read', t);
    EXECUTE format($p$CREATE POLICY %I ON %I FOR INSERT WITH CHECK (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))$p$, t || '_insert', t);
    EXECUTE format($p$CREATE POLICY %I ON %I FOR UPDATE USING (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))
      WITH CHECK (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))$p$, t || '_update', t);
    EXECUTE format($p$CREATE POLICY %I ON %I FOR DELETE USING (actor_has_permission(tenant_id, current_user_id(), 'integration.manage'))$p$, t || '_delete', t);
  END LOOP;
END $$;

-- Grants. The application role gets what the administration needs and NOT the digest: column-level SELECT leaves secret_digest out, so a
-- listing (or a bug that does SELECT *) can never return it. Authentication reads it through the SECURITY DEFINER function below.
-- default privileges of earlier migrations hand omnira_app every privilege on every new table, so the column-level grant below would restrict nothing
-- unless the table-level ones are taken away first (a test as omnira_app proves the digest is unreadable)
REVOKE ALL ON external_app_installations, external_app_installation_queues, external_app_installation_channels, external_app_credentials FROM PUBLIC, omnira_app;
GRANT SELECT, INSERT, UPDATE, DELETE ON external_app_installations, external_app_installation_queues, external_app_installation_channels TO omnira_app;
GRANT SELECT (id, tenant_id, installation_id, key_id, label, created_by, created_at, expires_at, revoked_at, last_used_at) ON external_app_credentials TO omnira_app;
GRANT INSERT ON external_app_credentials TO omnira_app;
GRANT UPDATE (revoked_at, last_used_at, expires_at) ON external_app_credentials TO omnira_app;

-- The doors the external API uses. Every one is SECURITY DEFINER with a pinned search_path, executable by the application role only, and
-- takes the tenant AND the installation (and the credential where it matters) so that a caller holding one identifier cannot touch another
-- tenant's rows. They are the ONLY way the digest, the lock and the allow-lists are reached from a system session.

-- (1) The only lookup that happens before a tenant is known. It returns the stored digest so the caller compares in constant time
-- (crypto/subtle); it returns nothing for an unknown, revoked or expired credential, a revoked or suspended installation, or a suspended
-- tenant. There is no tenant argument: the tenant is a property of the installation.
CREATE FUNCTION external_credential_lookup(p_key_id TEXT)
RETURNS TABLE (credential_id UUID, installation_id UUID, tenant_id UUID, secret_digest BYTEA, status TEXT, scopes TEXT[],
               rate_limit_per_min INTEGER, lease_seconds INTEGER, allow_outside_hours BOOLEAN, installation_name TEXT)
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT c.id, i.id, i.tenant_id, c.secret_digest, i.status, i.scopes, i.rate_limit_per_min, i.lease_seconds, i.allow_outside_hours, i.name
    FROM external_app_credentials c
    JOIN external_app_installations i ON i.tenant_id = c.tenant_id AND i.id = c.installation_id
    JOIN tenants t ON t.id = i.tenant_id
   WHERE c.key_id = p_key_id
     AND c.revoked_at IS NULL AND c.expires_at > now()
     AND i.status = 'active'
     AND t.status = 'active'
$$;

-- (2) last_used_at is bookkeeping, bound to the credential's own installation and tenant (an identifier of another tenant's credential
-- changes nothing), at most once a minute.
CREATE FUNCTION external_credential_touch(p_tenant UUID, p_installation UUID, p_credential UUID)
RETURNS VOID
LANGUAGE sql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  UPDATE external_app_credentials SET last_used_at = now()
   WHERE tenant_id = p_tenant AND installation_id = p_installation AND id = p_credential
     AND (last_used_at IS NULL OR last_used_at < now() - interval '1 minute')
$$;

-- (3) The second look of every mutation (ADR-0046): installation, credential AND company must be usable NOW, and the three rows stay locked FOR SHARE
-- until the caller's transaction ends (locks belong to the transaction, not to the function). Revoking, suspending or suspending the company takes a
-- conflicting lock, so it either waits for the write to commit or happens first and the write is refused. Expiry is a time, not a lock: it is judged
-- with clock_timestamp() (now() is the START of the transaction), at the moment of the recheck; a write that already passed it can commit up to its own
-- remaining duration after the expiry (bounded by the statement/transaction timeouts). Expiry is a retention bound; revocation is the immediate tool.
-- The functions run with the privileges of their owner, the migrating role (a superuser, which is why they can read the tables the policies hide).
CREATE FUNCTION external_recheck(p_tenant UUID, p_installation UUID, p_credential UUID)
RETURNS BOOLEAN
LANGUAGE plpgsql VOLATILE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
BEGIN
  PERFORM 1
     FROM external_app_installations i
     JOIN external_app_credentials c ON c.tenant_id = i.tenant_id AND c.installation_id = i.id
     JOIN tenants t ON t.id = i.tenant_id
    WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
      AND c.id = p_credential AND c.revoked_at IS NULL AND c.expires_at > clock_timestamp()
      AND t.status = 'active'
      FOR SHARE OF i, c, t;
  RETURN FOUND;
END
$$;

-- (4) The allow-lists of an active installation (empty arrays mean "every queue / every line of the tenant").
CREATE FUNCTION external_installation_allowlists(p_tenant UUID, p_installation UUID)
RETURNS TABLE (queue_ids TEXT[], channel_ids TEXT[])
LANGUAGE sql STABLE SECURITY DEFINER
SET search_path = pg_catalog, public, pg_temp
AS $$
  SELECT COALESCE((SELECT array_agg(q.queue_id::text ORDER BY q.queue_id) FROM external_app_installation_queues q
                    WHERE q.tenant_id = i.tenant_id AND q.installation_id = i.id), ARRAY[]::TEXT[]),
         COALESCE((SELECT array_agg(ch.connection_id::text ORDER BY ch.connection_id) FROM external_app_installation_channels ch
                    WHERE ch.tenant_id = i.tenant_id AND ch.installation_id = i.id), ARRAY[]::TEXT[])
    FROM external_app_installations i
    JOIN tenants t ON t.id = i.tenant_id AND t.status = 'active'
   WHERE i.tenant_id = p_tenant AND i.id = p_installation AND i.status = 'active'
$$;

REVOKE ALL ON FUNCTION external_credential_lookup(TEXT), external_credential_touch(UUID, UUID, UUID),
                       external_recheck(UUID, UUID, UUID), external_installation_allowlists(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION external_credential_lookup(TEXT), external_credential_touch(UUID, UUID, UUID),
                          external_recheck(UUID, UUID, UUID), external_installation_allowlists(UUID, UUID) TO omnira_app;
