DELETE FROM role_permissions WHERE permission_key IN
  ('flow.view','flow.create','flow.edit','flow.test','flow.publish','flow.archive','flow_template.view','flow_template.install','flow_run.view');
DELETE FROM permissions WHERE key IN
  ('flow.view','flow.create','flow.edit','flow.test','flow.publish','flow.archive','flow_template.view','flow_template.install','flow_run.view');
