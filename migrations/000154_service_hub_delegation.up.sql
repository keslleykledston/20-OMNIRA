-- ADR-0049 phase 5: a Hub agent attending a company reaches Service Context & Operations through the SAME rules as every other delegated domain (ADR-0040):
--   * a new data domain `service`: service.read opens READING; create/update/close/assign/link_external open WRITING. `service.request_external` is deliberately NOT
--     in permission_domains, so it is never delegable, whatever a contract or a grant says: asking an ERP to do something is the company's own act.
--   * the delegate is NOT a member. Reading is by `delegated_tenants('service','read')`; writing goes through the write policies of 000150-000153, whose
--     actor_has_permission() answers from the live grant in a delegated context.
--   * ONE instance at a time, and the visibility of the conversation: a restrictive policy per table pins the request to the instance it acts for and to the
--     conversations the contract's queue scope lets the delegate see (a session without a visible conversation, a case without a visible session, a task, record
--     or link that hangs from nothing visible are all invisible to a delegate). Members are untouched (`acting_hub() IS NULL`).
--   * the requests made to the ERP (external_record_requests) are closed to a delegate altogether.
SET LOCAL lock_timeout = '5s';

ALTER TABLE permission_domains DROP CONSTRAINT permission_domains_domain_check;
ALTER TABLE permission_domains ADD CONSTRAINT permission_domains_domain_check CHECK (domain IN ('conversation', 'media', 'contact', 'ticket', 'flow', 'ai', 'service'));
INSERT INTO permission_domains (permission_key, domain, need) VALUES
  ('service.read',          'service', 'read'),
  ('service.create',        'service', 'write'),
  ('service.update',        'service', 'write'),
  ('service.close',         'service', 'write'),
  ('service.assign',        'service', 'write'),
  ('service.link_external', 'service', 'write');

-- Visibility helpers. SECURITY INVOKER on purpose: they ask the row-level rules of the CALLER, so a delegate sees exactly what its own policies let it see.
CREATE FUNCTION service_conversation_visible(p_tenant UUID, p_id UUID) RETURNS BOOLEAN AS $$
  SELECT p_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.conversations c WHERE c.tenant_id = p_tenant AND c.id = p_id);
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;
CREATE FUNCTION service_contact_visible(p_tenant UUID, p_id UUID) RETURNS BOOLEAN AS $$
  SELECT p_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.contacts c WHERE c.tenant_id = p_tenant AND c.id = p_id);
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;
CREATE FUNCTION service_session_visible(p_tenant UUID, p_id UUID) RETURNS BOOLEAN AS $$
  SELECT p_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.service_sessions s WHERE s.tenant_id = p_tenant AND s.id = p_id);
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;
CREATE FUNCTION service_case_visible(p_tenant UUID, p_id UUID) RETURNS BOOLEAN AS $$
  SELECT p_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.service_cases c WHERE c.tenant_id = p_tenant AND c.id = p_id);
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;
CREATE FUNCTION service_task_visible(p_tenant UUID, p_id UUID) RETURNS BOOLEAN AS $$
  SELECT p_id IS NOT NULL AND EXISTS (SELECT 1 FROM public.service_tasks t WHERE t.tenant_id = p_tenant AND t.id = p_id);
$$ LANGUAGE SQL STABLE SET search_path = pg_catalog, public, pg_temp;

-- The provider of one of the company's integrations (an ERP connection), for whoever may READ the module. The connection table itself stays closed to a delegate
-- (it holds credentials); this answers only the provider name, only for the instance the request acts for, only for somebody who holds service.read.
CREATE FUNCTION service_integration_system(p_tenant UUID, p_integration UUID) RETURNS TEXT AS $$
  SELECT lower(c.provider)
  FROM public.channel_connections c
  WHERE c.tenant_id = p_tenant AND c.id = p_integration
    AND ( public.is_system_admin()
          OR (public.acting_hub() IS NULL AND p_tenant IN (SELECT public.service_read_tenants()))
          OR (public.acting_hub() IS NOT NULL AND p_tenant = public.acting_tenant() AND public.actor_has_permission(p_tenant, public.current_user_id(), 'service.read')) );
$$ LANGUAGE SQL STABLE SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION service_integration_system(UUID, UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION service_integration_system(UUID, UUID) TO omnira_app;

-- 1. reading: by the data domain, for the instances the grant covers (the restrictive policies below narrow it to one instance and to what is visible)
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['service_sessions', 'service_session_episodes', 'service_cases', 'service_case_sessions', 'service_tasks', 'context_events',
                           'external_records', 'service_external_links', 'service_categories', 'service_resolution_codes', 'service_command_receipts'] LOOP
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (tenant_id IN (SELECT delegated_tenants(''service'', ''read'')))', t || '_read_delegated', t);
  END LOOP;
END $$;
-- the protocol counters are only touched to take a number: whoever may create takes it
CREATE POLICY service_counters_read_delegated ON service_counters FOR SELECT USING (tenant_id IN (SELECT delegated_tenants('service', 'write')));

-- 2. one instance at a time + what is visible, for every command (a FOR ALL restrictive policy checks the new row of an INSERT/UPDATE too)
CREATE POLICY service_sessions_acting_scope ON service_sessions AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND service_conversation_visible(tenant_id, conversation_id)));
CREATE POLICY service_session_episodes_acting_scope ON service_session_episodes AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND service_session_visible(tenant_id, session_id)));
CREATE POLICY service_cases_acting_scope ON service_cases AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND (
           service_session_visible(tenant_id, origin_session_id)
           OR EXISTS (SELECT 1 FROM service_case_sessions l WHERE l.tenant_id = service_cases.tenant_id AND l.case_id = service_cases.id AND service_session_visible(l.tenant_id, l.session_id)))));
-- (a link is visible by its SESSION alone: a case is visible BECAUSE a link ties it to a visible session, so checking the case here would make the two policies call each other)
CREATE POLICY service_case_sessions_acting_scope ON service_case_sessions AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND service_session_visible(tenant_id, session_id)));
CREATE POLICY service_tasks_acting_scope ON service_tasks AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND (
           service_session_visible(tenant_id, session_id) OR service_case_visible(tenant_id, case_id) OR service_contact_visible(tenant_id, contact_id))));
CREATE POLICY context_events_acting_scope ON context_events AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND (
           service_session_visible(tenant_id, session_id) OR service_case_visible(tenant_id, case_id) OR service_contact_visible(tenant_id, contact_id))));
CREATE POLICY service_external_links_acting_scope ON service_external_links AS RESTRICTIVE
  USING (acting_hub() IS NULL OR (tenant_id = acting_tenant() AND (
           service_session_visible(tenant_id, session_id) OR service_case_visible(tenant_id, case_id) OR service_task_visible(tenant_id, task_id))));
-- a record is seen through the links that hang from something visible; it can be INSERTED before it has a link (the link comes right after), so only reading and updating need the link
CREATE POLICY external_records_acting_pin ON external_records AS RESTRICTIVE
  USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY external_records_acting_visible_read ON external_records AS RESTRICTIVE FOR SELECT
  USING (acting_hub() IS NULL OR EXISTS (SELECT 1 FROM service_external_links l WHERE l.tenant_id = external_records.tenant_id AND l.external_record_id = external_records.id AND l.unlinked_at IS NULL));
CREATE POLICY external_records_acting_visible_update ON external_records AS RESTRICTIVE FOR UPDATE
  USING (acting_hub() IS NULL OR EXISTS (SELECT 1 FROM service_external_links l WHERE l.tenant_id = external_records.tenant_id AND l.external_record_id = external_records.id AND l.unlinked_at IS NULL));
-- tenant-level tables: only the instance pin
CREATE POLICY service_categories_acting_pin ON service_categories AS RESTRICTIVE USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY service_resolution_codes_acting_pin ON service_resolution_codes AS RESTRICTIVE USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY service_counters_acting_pin ON service_counters AS RESTRICTIVE USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
CREATE POLICY service_command_receipts_acting_pin ON service_command_receipts AS RESTRICTIVE USING (acting_hub() IS NULL OR tenant_id = acting_tenant());
-- the requests made to the ERP are the company's own act: closed to any delegate, for reading and writing
CREATE POLICY external_record_requests_acting_closed ON external_record_requests AS RESTRICTIVE USING (acting_hub() IS NULL);
