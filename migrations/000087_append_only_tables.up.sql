-- ADR-0019/0020: make the documented immutability real. Migration 000006 grants INSERT/SELECT/UPDATE/DELETE on every new table to
-- omnira_app through default privileges, so the minimal GRANTs in 000082 and 000086 never restricted anything. These tables are
-- append-only for the application (no code path updates or deletes them); cascades from the parent tables (tenant, flow, run,
-- conversation) are executed by the table owner and are not affected.
REVOKE UPDATE, DELETE ON conversation_closures, flow_versions, flow_node_executions, flow_pack_installations, flow_template_installations FROM omnira_app;

-- Defense in depth for the closure record (flow_versions already has the same trigger): not even the owner can rewrite one.
CREATE FUNCTION conversation_closures_immutable() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
  RAISE EXCEPTION 'conversation_closures are immutable' USING ERRCODE = 'integrity_constraint_violation';
END $$;
CREATE TRIGGER conversation_closures_no_update BEFORE UPDATE ON conversation_closures FOR EACH ROW EXECUTE FUNCTION conversation_closures_immutable();
