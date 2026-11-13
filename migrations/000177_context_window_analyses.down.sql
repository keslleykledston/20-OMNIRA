DROP TABLE IF EXISTS context_proposed_fact_evidence;
DROP TRIGGER IF EXISTS context_proposed_facts_immutable_core ON context_proposed_facts;
DROP TABLE IF EXISTS context_proposed_facts;
DROP FUNCTION IF EXISTS context_proposed_facts_guard_immutable_fields();
DROP TABLE IF EXISTS context_window_analyses;
