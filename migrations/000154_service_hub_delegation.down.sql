-- Rolls back ADR-0049 phase 5: no Hub agent reaches the module any more. Safe: it only removes policies, helper functions and delegable keys.
SET LOCAL lock_timeout = '5s';
DROP POLICY IF EXISTS external_record_requests_acting_closed ON external_record_requests;
DROP POLICY IF EXISTS service_command_receipts_acting_pin ON service_command_receipts;
DROP POLICY IF EXISTS service_counters_acting_pin ON service_counters;
DROP POLICY IF EXISTS service_resolution_codes_acting_pin ON service_resolution_codes;
DROP POLICY IF EXISTS service_categories_acting_pin ON service_categories;
DROP POLICY IF EXISTS external_records_acting_visible_update ON external_records;
DROP POLICY IF EXISTS external_records_acting_visible_read ON external_records;
DROP POLICY IF EXISTS external_records_acting_pin ON external_records;
DROP POLICY IF EXISTS service_external_links_acting_scope ON service_external_links;
DROP POLICY IF EXISTS context_events_acting_scope ON context_events;
DROP POLICY IF EXISTS service_tasks_acting_scope ON service_tasks;
DROP POLICY IF EXISTS service_case_sessions_acting_scope ON service_case_sessions;
DROP POLICY IF EXISTS service_cases_acting_scope ON service_cases;
DROP POLICY IF EXISTS service_session_episodes_acting_scope ON service_session_episodes;
DROP POLICY IF EXISTS service_sessions_acting_scope ON service_sessions;
DROP POLICY IF EXISTS service_counters_read_delegated ON service_counters;
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['service_sessions', 'service_session_episodes', 'service_cases', 'service_case_sessions', 'service_tasks', 'context_events',
                           'external_records', 'service_external_links', 'service_categories', 'service_resolution_codes', 'service_command_receipts'] LOOP
    EXECUTE format('DROP POLICY IF EXISTS %I ON %I', t || '_read_delegated', t);
  END LOOP;
END $$;
DROP FUNCTION IF EXISTS service_integration_system(UUID, UUID);
DROP FUNCTION IF EXISTS service_task_visible(UUID, UUID);
DROP FUNCTION IF EXISTS service_case_visible(UUID, UUID);
DROP FUNCTION IF EXISTS service_session_visible(UUID, UUID);
DROP FUNCTION IF EXISTS service_contact_visible(UUID, UUID);
DROP FUNCTION IF EXISTS service_conversation_visible(UUID, UUID);
DELETE FROM permission_domains WHERE domain = 'service';
ALTER TABLE permission_domains DROP CONSTRAINT permission_domains_domain_check;
ALTER TABLE permission_domains ADD CONSTRAINT permission_domains_domain_check CHECK (domain IN ('conversation', 'media', 'contact', 'ticket', 'flow', 'ai'));
