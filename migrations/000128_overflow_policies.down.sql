-- OPERATIONAL GATE: backup and the owner's approval first. DATA LOSS: drops every overflow policy and step.
DROP TABLE IF EXISTS overflow_steps;
DROP TABLE IF EXISTS overflow_policies;
DELETE FROM role_permissions WHERE permission_key = 'routing.override';
DELETE FROM permissions WHERE key = 'routing.override';
