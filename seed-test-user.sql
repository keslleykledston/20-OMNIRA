-- Seed de usuário e tenant de teste para OMNIRA

-- 1. Criar Tenant
INSERT INTO tenants (id, name, status, plan, created_at, updated_at)
VALUES (
  '00000000-0000-0000-0000-000000000001'::uuid,
  'Test Tenant',
  'active',
  'standard',
  NOW(),
  NOW()
)
ON CONFLICT DO NOTHING;

-- 2. Criar User (external_subject = "test@omnira.local")
INSERT INTO users (id, external_subject, email, status, created_at, updated_at)
VALUES (
  '00000000-0000-0000-0000-000000000002'::uuid,
  'test@omnira.local',
  'test@omnira.local',
  'active',
  NOW(),
  NOW()
)
ON CONFLICT DO NOTHING;

-- 3. Criar Membership (User → Tenant)
INSERT INTO memberships (id, user_id, tenant_id, roles, status, created_at, updated_at)
VALUES (
  '00000000-0000-0000-0000-000000000003'::uuid,
  '00000000-0000-0000-0000-000000000002'::uuid,
  '00000000-0000-0000-0000-000000000001'::uuid,
  ARRAY['admin']::text[],
  'active',
  NOW(),
  NOW()
)
ON CONFLICT DO NOTHING;

-- Verifica dados criados
SELECT 'Tenants:' as info;
SELECT id, name, status FROM tenants LIMIT 1;

SELECT 'Users:' as info;
SELECT id, email, status FROM users LIMIT 1;

SELECT 'Memberships:' as info;
SELECT id, user_id, tenant_id, roles FROM memberships LIMIT 1;
