DROP TABLE IF EXISTS flow_template_installations;
DROP TABLE IF EXISTS flow_pack_installations;
DROP TABLE IF EXISTS flow_node_executions;
DROP TABLE IF EXISTS flow_runs;
ALTER TABLE IF EXISTS flows DROP CONSTRAINT IF EXISTS flows_active_version_fk;
DROP TABLE IF EXISTS flow_versions;
DROP FUNCTION IF EXISTS flow_versions_immutable();
DROP TABLE IF EXISTS flows;
