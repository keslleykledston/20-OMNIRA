-- ADR-0018 addendum (Interno): who may see the conversations of contacts an operator declared internal (team on a personal
-- number, partners, suppliers). Those conversations are not customer service; they stay out of the agents' reach unless the
-- role says otherwise.
--
--   conversation.read_internal: read and operate (reply, assign, ticket, notes) a conversation whose contact has kind='internal'.
--
-- Default grants: administrators and supervisors. Agents do NOT receive it. The roles are the fixed global ones, so this is the
-- policy for every company. Enforced by the route middleware (internal/tenancy/adapters/internal_conversations.go) and by the
-- inbox list; a delegated (Hub) context never holds it, because the key is not delegable.
INSERT INTO permissions(key, description) VALUES
  ('conversation.read_internal', 'Read and operate conversations with contacts declared internal (team, partners, suppliers)')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key = 'conversation.read_internal'
ON CONFLICT DO NOTHING;
