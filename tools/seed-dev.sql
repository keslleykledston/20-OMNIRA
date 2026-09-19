-- DEVELOPMENT SEED (idempotent). Do NOT run in production.
-- Creates what the backend's mock login knows (internal/platform/authn/mock_login.go):
--   test@omnira.local  -> agent   of tenant 11111111-1111-1111-1111-111111111111
--   admin@omnira.local -> admin   of the same tenant
-- plus a manual default queue. Real users/tenants need a real IdP + onboarding (pending, see
-- docs/delivery/ROADMAP-TO-GOAL.md).
INSERT INTO tenants (id, legal_name, status)
VALUES ('11111111-1111-1111-1111-111111111111', 'Test Company LTDA', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO users (id, external_subject, email, status) VALUES
  ('22222222-2222-2222-2222-222222222222', 'test@omnira.local',  'test@omnira.local',  'active'),
  ('aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', 'admin@omnira.local', 'admin@omnira.local', 'active')
ON CONFLICT (id) DO NOTHING;

INSERT INTO memberships (tenant_id, user_id, role_id, status)
SELECT '11111111-1111-1111-1111-111111111111', '22222222-2222-2222-2222-222222222222', id, 'active'
FROM roles WHERE key = 'tenant_agent' AND tenant_id IS NULL
ON CONFLICT (tenant_id, user_id) DO NOTHING;

INSERT INTO memberships (tenant_id, user_id, role_id, status)
SELECT '11111111-1111-1111-1111-111111111111', 'aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa', id, 'active'
FROM roles WHERE key = 'tenant_admin' AND tenant_id IS NULL
ON CONFLICT (tenant_id, user_id) DO NOTHING;

INSERT INTO queues (tenant_id, name, mode, is_default)
VALUES ('11111111-1111-1111-1111-111111111111', 'Default', 'manual', true)
ON CONFLICT (tenant_id, name) DO NOTHING;
