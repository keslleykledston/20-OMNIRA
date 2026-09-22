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

-- IAM4.2 pilot fixture: round-robin queue + operational AgentProfile for
-- test@omnira.local, so presence-aware routing (routing_require_presence,
-- IAM4.2-B1) can be smoke-tested in dev without it being enabled. Queue CRUD
-- (queue.manage) is still deferred product-side — this exists only for
-- dev/test/pilot validation, same idempotent pattern as the rest of this file.
INSERT INTO queues (id, tenant_id, name, mode, is_default)
VALUES ('eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', '11111111-1111-1111-1111-111111111111', 'Pilot Round Robin', 'round_robin', false)
ON CONFLICT (tenant_id, name) DO NOTHING;

INSERT INTO agent_profiles (tenant_id, membership_id, status)
SELECT '11111111-1111-1111-1111-111111111111', m.id, 'active'
FROM memberships m
WHERE m.tenant_id = '11111111-1111-1111-1111-111111111111'
  AND m.user_id = '22222222-2222-2222-2222-222222222222'
ON CONFLICT (tenant_id, membership_id) DO NOTHING;

INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity)
VALUES ('11111111-1111-1111-1111-111111111111', 'eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee', '22222222-2222-2222-2222-222222222222', true, true, 1)
ON CONFLICT (tenant_id, queue_id, user_id) DO NOTHING;
