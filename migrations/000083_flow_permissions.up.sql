-- FLOW.2 (ADR-0019): permission keys for the Flow Builder. Same pattern as ticket.*/group.*: this migration only
-- registers keys and default grants; nothing existing is altered. Editing and publishing are separate on purpose.
INSERT INTO permissions(key, description) VALUES
  ('flow.view',            'View flows, versions and drafts'),
  ('flow.create',          'Create flows (draft only)'),
  ('flow.edit',            'Edit a flow draft'),
  ('flow.test',            'Run a flow in the simulator (no real side effects)'),
  ('flow.publish',         'Publish a flow version and switch the active version (rollback)'),
  ('flow.archive',         'Archive a flow'),
  ('flow_template.view',   'Browse system flow templates and packs'),
  ('flow_template.install','Install a flow template or pack as tenant-owned drafts'),
  ('flow_run.view',        'View flow runs and their execution timeline')
ON CONFLICT (key) DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key = 'tenant_admin' AND p.key IN
  ('flow.view','flow.create','flow.edit','flow.test','flow.publish','flow.archive','flow_template.view','flow_template.install','flow_run.view')
ON CONFLICT DO NOTHING;

INSERT INTO role_permissions(role_id, permission_key)
SELECT r.id, p.key FROM roles r CROSS JOIN permissions p
WHERE r.tenant_id IS NULL AND r.key = 'tenant_supervisor' AND p.key IN
  ('flow.view','flow.test','flow_template.view','flow_run.view')
ON CONFLICT DO NOTHING;
