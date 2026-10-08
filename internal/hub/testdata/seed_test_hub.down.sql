-- Rollback test fixtures

DELETE FROM hub_inbox_items
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM effective_access_grants
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM agent_skills
WHERE skill_id IN (
  SELECT id FROM skills
  WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID
);

DELETE FROM skills
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM work_pool_members
WHERE work_pool_id IN (
  SELECT id FROM work_pools
  WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID
);

DELETE FROM work_pools
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM hub_tenant_service_contracts
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM hub_memberships
WHERE hub_id = '550e8400-e29b-41d4-a716-446655440001'::UUID;

DELETE FROM service_hubs
WHERE id = '550e8400-e29b-41d4-a716-446655440001'::UUID;
