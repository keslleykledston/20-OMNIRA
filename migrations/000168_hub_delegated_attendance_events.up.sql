-- ADR-0040: a Hub agent serving an instance reads the TIMELINE of an attendance (GET .../inbox/conversations/{id}/timeline: taken, assigned, released, transferred,
-- sent to a queue, an external application claimed/released it). Those facts live in assignment_events and conversation_control_events, whose read policy is the
-- member's alone, so a delegated reader got a thread with the lines missing.
--
-- The rule is the one of the messages: the delegated context reads them for THE instance it acts for, when the grant holds the conversation domain (key
-- conversation.read) and the conversation they hang from is visible to it (its contract's queue scope included). SELECT only; nothing here lets a delegate write, and the
-- closed/open facts that carry a person's words stay behind their own key (conversation_closures: attendance.history, migration 117). A restrictive policy keeps the
-- reading to one instance at a time, like every other delegated table.

CREATE POLICY assignment_events_read_acting ON assignment_events FOR SELECT
  USING ((SELECT acting_hub()) IS NOT NULL
         AND tenant_id = (SELECT acting_tenant())
         AND tenant_id IN (SELECT delegated_tenants('conversation', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = assignment_events.conversation_id AND c.tenant_id = assignment_events.tenant_id));

CREATE POLICY conversation_control_events_read_acting ON conversation_control_events FOR SELECT
  USING ((SELECT acting_hub()) IS NOT NULL
         AND tenant_id = (SELECT acting_tenant())
         AND tenant_id IN (SELECT delegated_tenants('conversation', 'read'))
         AND EXISTS (SELECT 1 FROM conversations c WHERE c.id = conversation_control_events.conversation_id AND c.tenant_id = conversation_control_events.tenant_id));

CREATE POLICY assignment_events_acting_one_instance ON assignment_events AS RESTRICTIVE FOR SELECT
  USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
CREATE POLICY conversation_control_events_acting_one_instance ON conversation_control_events AS RESTRICTIVE FOR SELECT
  USING ((SELECT acting_hub()) IS NULL OR tenant_id = (SELECT acting_tenant()));
