-- ADR-0015: who may read the groups an administrator enabled, and who may enable/disable them
-- and delete their history. Mirrors 000043 (ticket.read): granted to admin and supervisor, not to
-- the plain agent, because a group is not part of attending a customer.
INSERT INTO permissions(key, description) VALUES
  ('group.read',   'Read WhatsApp group messages of the groups an administrator enabled'),
  ('group.manage', 'Enable or disable WhatsApp groups and delete their stored history')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key IN ('group.read','group.manage')
ON CONFLICT DO NOTHING;
