DROP TRIGGER IF EXISTS conversation_closures_no_update ON conversation_closures;
DROP FUNCTION IF EXISTS conversation_closures_immutable();
GRANT UPDATE, DELETE ON conversation_closures, flow_versions, flow_node_executions, flow_pack_installations, flow_template_installations TO omnira_app;
