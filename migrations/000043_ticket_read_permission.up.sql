-- PRODUCT.2-A: permission foundation for canonical tenant-wide ticket
-- listing (blocked PRODUCT.2 — no ticket.* permission existed). This slice
-- only registers the permission and its default grants; no ticket schema,
-- RLS, index, or endpoint change.
--   ticket.read: read/list canonical tickets across the current tenant
--   (evaluated inside explicit TenantContext, never a cross-tenant or RLS
--   bypass grant). Broader than conversation.claim/conversation.manage
--   (which govern acting on one conversation), so it is not granted to
--   tenant_agent by default.
INSERT INTO permissions(key, description) VALUES
  ('ticket.read', 'Read tenant-wide canonical tickets')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key = 'ticket.read'
ON CONFLICT DO NOTHING;
