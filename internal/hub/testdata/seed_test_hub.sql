-- Test fixtures for Hub testing (development only)
-- Note: This migration can be safely skipped in production via .env flag

-- Insert test Service Hub
INSERT INTO service_hubs (id, name, status, created_at) VALUES
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'K3G Service Desk (Test)', 'active', now());

-- Assume test users + tenants exist (populated by earlier migrations)
-- Get IDs from existing data dynamically if possible, but fallback to nulls for now

-- Insert Hub memberships (linking users to test Hub)
-- Note: In real tests, these will be created dynamically
INSERT INTO hub_memberships (hub_id, user_id, role_id)
SELECT
  '550e8400-e29b-41d4-a716-446655440001'::UUID,
  u.id,
  r.id
FROM users u
CROSS JOIN roles r
WHERE u.created_at > now() - interval '1 hour'  -- Recent test users
  AND r.key = 'hub_agent'
  AND u.email LIKE '%+hub%' -- Marker for test users
ON CONFLICT (hub_id, user_id) DO NOTHING;

-- Insert test Service Contracts
INSERT INTO hub_tenant_service_contracts (hub_id, tenant_id, status, service_scope)
SELECT
  '550e8400-e29b-41d4-a716-446655440001'::UUID,
  t.id,
  'active',
  jsonb_build_object(
    'queues', jsonb_build_array('default', 'support'),
    'capabilities', jsonb_build_array('read', 'write'),
    'max_agents', 10
  )
FROM tenants t
WHERE t.created_at > now() - interval '1 hour'  -- Recent test tenants
  AND t.name LIKE '%Test%'
ON CONFLICT (hub_id, tenant_id) DO NOTHING;

-- Insert Work Pools
INSERT INTO work_pools (hub_id, name, description)
VALUES
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'Support Level 1', 'First line support'),
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'Support Level 2', 'Escalation'),
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'Billing', 'Billing queries')
ON CONFLICT DO NOTHING;

-- Insert Skills
INSERT INTO skills (hub_id, key, name, description)
VALUES
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'whatsapp', 'WhatsApp Support', 'Can handle WhatsApp conversations'),
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'email', 'Email Support', 'Can handle email inquiries'),
  ('550e8400-e29b-41d4-a716-446655440001'::UUID, 'billing', 'Billing Expert', 'Handles billing and invoicing')
ON CONFLICT DO NOTHING;
