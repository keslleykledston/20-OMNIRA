-- PRODUCT.7A1 (PRODUCT.7A audit): permission foundation for a read-only
-- tenant ticket reconciliation/audit surface over the durable
-- ticket_external_create_attempts / ticket_external_status_attempts
-- tables. Same pattern as migrations 000043 (ticket.read), 000046
-- (ticket.create) and 000049 (ticket.update): this slice only registers
-- the permission and its default grants — no application service,
-- endpoint, or RLS change happens in this migration.
--
--   ticket.reconcile: read durable external-ticket integration attempt
--   records (create and status mutation attempts) for the current tenant
--   — actor identity, idempotency/reconciliation state, provider write
--   outcome. Deliberately NOT ticket.update (mutating an already-linked
--   ticket's lifecycle from one's own conversation) and NOT ticket.read
--   (browsing canonical tickets): this exposes a materially more
--   sensitive, cross-agent operational view (every actor's identity,
--   redacted idempotency state) that neither existing permission implies.
--   Never granted to tenant_agent — this is a support/admin capability,
--   not an operating-agent one.
INSERT INTO permissions(key, description) VALUES
  ('ticket.reconcile', 'Read durable external ticket integration attempt records (create and status mutation) for the current tenant')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor')
  AND p.key = 'ticket.reconcile'
ON CONFLICT DO NOTHING;
