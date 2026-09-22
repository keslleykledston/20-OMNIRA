-- IAM4.1: optional operational extension of a tenant membership.
ALTER TABLE memberships ADD CONSTRAINT memberships_tenant_id_id_uq UNIQUE (tenant_id, id);

CREATE TABLE agent_profiles (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  membership_id UUID NOT NULL,
  status TEXT NOT NULL DEFAULT 'active' CHECK (status IN ('active', 'disabled')),
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, membership_id),
  FOREIGN KEY (tenant_id, membership_id) REFERENCES memberships(tenant_id, id) ON DELETE RESTRICT
);

ALTER TABLE agent_profiles ENABLE ROW LEVEL SECURITY;
ALTER TABLE agent_profiles FORCE ROW LEVEL SECURITY;
CREATE POLICY agent_profiles_read_tenant ON agent_profiles FOR SELECT
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY agent_profiles_insert_tenant ON agent_profiles FOR INSERT
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
CREATE POLICY agent_profiles_update_tenant ON agent_profiles FOR UPDATE
  USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())
  WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin());
GRANT SELECT, INSERT, UPDATE ON agent_profiles TO omnira_app;
REVOKE DELETE ON agent_profiles FROM omnira_app;

-- Queue membership is the only operational evidence allowed for backfill.
DO $iam4$
BEGIN
  IF EXISTS (
    SELECT 1 FROM queue_members qm
    LEFT JOIN memberships m ON m.tenant_id=qm.tenant_id AND m.user_id=qm.user_id
    WHERE m.id IS NULL
  ) THEN
    RAISE EXCEPTION 'IAM4 backfill blocked: queue_members without same-tenant membership';
  END IF;
END $iam4$;

INSERT INTO agent_profiles (tenant_id, membership_id, status)
SELECT DISTINCT qm.tenant_id, m.id, 'active'
FROM queue_members qm
JOIN memberships m ON m.tenant_id=qm.tenant_id AND m.user_id=qm.user_id
ON CONFLICT (tenant_id, membership_id) DO NOTHING;

INSERT INTO permissions(key, description) VALUES
  ('agent.read', 'Read operational agents'),
  ('agent.manage', 'Manage operational agents and queue eligibility')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key IN ('agent.read','agent.manage')
ON CONFLICT DO NOTHING;
