-- PRODUCT.6-F (ADR-0013): permission foundation for a future real
-- ticket-creation flow (CreateExternalTicket application service — not
-- implemented yet; the K3G write contract is still pending clarification,
-- see PRODUCT.6-C1/6-C2). Same pattern as PRODUCT.3-A's dashboard.read:
-- this slice only registers the permission and its default grants. No
-- handler checks this permission key yet, so granting it now is inert.
--
--   ticket.create: create a ticket FROM A CONVERSATION THE ACTOR IS ALREADY
--   AUTHORIZED TO OPERATE — never tenant-wide ticket authoring, never
--   implying ticket.read. The intended enforcement (not implemented in
--   this slice) is the same two-part rule internal/messages/application's
--   Sender already uses for replying to a conversation: hold
--   conversation.claim as a baseline, then be the conversation's assignee
--   OR additionally hold conversation.manage. tenant_agent deliberately
--   does NOT receive ticket.read here (see migration 000043's own
--   rationale) — "create a ticket on my own conversation" and "browse
--   every tenant ticket" are different capabilities.
INSERT INTO permissions(key, description) VALUES
  ('ticket.create', 'Create a ticket from a conversation the actor is authorized to operate')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key IN ('tenant_admin','tenant_supervisor','tenant_agent')
  AND p.key = 'ticket.create'
ON CONFLICT DO NOTHING;
