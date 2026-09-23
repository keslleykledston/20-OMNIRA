-- PRODUCT.3-A: permission foundation for a future real tenant-wide Dashboard
-- snapshot. This slice only registers the permission and its default
-- grants; no Dashboard schema, endpoint, or frontend change.
--   dashboard.read: read tenant-wide operational Dashboard aggregates,
--   evaluated inside explicit TenantContext (never a cross-tenant or RLS
--   bypass grant). Active membership alone is not sufficient — a
--   tenant_agent must not automatically gain tenant-wide operational
--   visibility merely by being an active member, so it is not granted
--   here.
INSERT INTO permissions(key, description) VALUES
  ('dashboard.read', 'Read tenant-wide operational Dashboard aggregates')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key = 'dashboard.read'
ON CONFLICT DO NOTHING;
