-- PRODUCT.6-O2B1 (ADR-0013 follow-up, PRODUCT.6-O2A/O2A2/O2A3/O2A4):
-- permission foundation for a future real ticket LIFECYCLE MUTATION flow
-- (status update against the tenant's ERP — K3G confirmed real via
-- PUT /api/support/tickets/{id}/status, PRODUCT.6-O2A3). No application
-- service or connector exists yet that checks this permission — this
-- slice only registers the permission and its default grants, same
-- pattern as migrations 000043 (ticket.read) and 000046 (ticket.create).
--
--   ticket.update: mutate the lifecycle/status of an already-linked
--   external ticket FROM A CONVERSATION THE ACTOR IS ALREADY AUTHORIZED TO
--   OPERATE — never tenant-wide ticket authoring/mutation, never implying
--   ticket.read or substituting for it. Neither ticket.create ("create a
--   ticket from my own conversation") nor ticket.read ("browse every
--   tenant ticket") is semantically correct for "change the lifecycle of
--   a ticket already linked to my conversation" (PRODUCT.6-O2A section 7)
--   — this is a distinct capability. The intended enforcement (not
--   implemented in this slice) is the same conversation-ownership rule
--   CreateExternalTicket/RefreshTicketProjection already use: be the
--   conversation's assignee OR additionally hold conversation.manage.
--   Granted to tenant_agent by default for the same reason ticket.create
--   is (PRODUCT.6-O2A section 2/13): an agent operating their own
--   conversation's ticket needs this, distinct from tenant-wide
--   ticket.read which tenant_agent still does not receive.
INSERT INTO permissions(key, description) VALUES
  ('ticket.update', 'Mutate the lifecycle/status of an already-linked external ticket from a conversation the actor is authorized to operate')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor','tenant_agent')
  AND p.key = 'ticket.update'
ON CONFLICT DO NOTHING;
