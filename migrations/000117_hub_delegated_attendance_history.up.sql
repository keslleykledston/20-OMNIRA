-- ADR-0040 phase 06: a Hub agent attending an instance may READ the memory of its contacts (the earlier attendances of a contact with their summary, and what
-- stayed pending or was promised) and RESOLVE an open item. Two new keys, both optional per grant (no role of any company receives them, no earlier preset has them):
--   attendance.history : read conversation_closures and follow_up_items of the contacts of the instance. Data domain `contact`, read: the key only has to be delegable
--                        and let the person see the contact; the policies below ask for the KEY itself. The summaries of earlier attendances are internal data of the
--                        company (what was said about the customer, what was promised), more than reading the card or a conversation, so no other key opens them.
--   followup.resolve   : mark an open item done or dropped. Data domain `contact`, read, for the same reason as contact.note (a `write` mapping would hand its
--                        holders the write policies of the contact domain at the database layer). The update policy asks for the key itself, only opens an OPEN item, only
--                        lets the person turn it into done/dropped signed by THEMSELVES, and needs attendance.history too (an UPDATE reads what it changes).
-- Closing an attendance (writing a closure and its follow-ups) stays in the Hub's write path (migration 116, system session): nothing here lets a delegate INSERT.
--
-- Every policy is RESTRICTIVE on top of the permissive ones that already exist (the legacy tenant policy of the attendance tables is keyed to an active membership, which a
-- Hub agent does not have; a person who is BOTH a member and acting for a hub must not read the history as a member while acting as an agent). In the acting context the
-- key is required by the database itself, and one instance at a time (migration 111).

INSERT INTO permissions (key, description) VALUES
  ('attendance.history', 'Read the earlier attendances and the open follow-ups of the contacts of an instance (delegated serving)'),
  ('followup.resolve',   'Mark a follow-up item of a contact as done or dropped (delegated serving)')
ON CONFLICT (key) DO NOTHING;

INSERT INTO permission_domains (permission_key, domain, need) VALUES
  ('attendance.history', 'contact', 'read'),
  ('followup.resolve',   'contact', 'read');

-- reading: delegated policies (visibility of the contact, queue scope included) ...
CREATE POLICY conversation_closures_read_delegated ON conversation_closures FOR SELECT
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'attendance.history')
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = conversation_closures.contact_id AND ct.tenant_id = conversation_closures.tenant_id));
CREATE POLICY follow_up_items_read_delegated ON follow_up_items FOR SELECT
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'attendance.history')
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = follow_up_items.contact_id AND ct.tenant_id = follow_up_items.tenant_id));

-- ... resolving: one open item, turned into done/dropped by the person themselves
CREATE POLICY follow_up_items_resolve_delegated ON follow_up_items FOR UPDATE
  USING (acting_hub() IS NOT NULL
         AND actor_has_permission(tenant_id, current_user_id(), 'followup.resolve')
         AND actor_has_permission(tenant_id, current_user_id(), 'attendance.history')
         AND status = 'open'
         AND EXISTS (SELECT 1 FROM contacts ct WHERE ct.id = follow_up_items.contact_id AND ct.tenant_id = follow_up_items.tenant_id))
  WITH CHECK (acting_hub() IS NOT NULL
              AND status IN ('done', 'dropped')
              AND resolved_by_user_id = current_user_id());

-- the key, asked of the database in the acting context, for every command; and one instance at a time
CREATE POLICY conversation_closures_acting_history ON conversation_closures AS RESTRICTIVE
  USING (acting_hub() IS NULL
         OR (tenant_id = acting_tenant() AND actor_has_permission(tenant_id, current_user_id(), 'attendance.history')));
CREATE POLICY follow_up_items_acting_history ON follow_up_items AS RESTRICTIVE
  USING (acting_hub() IS NULL
         OR (tenant_id = acting_tenant() AND actor_has_permission(tenant_id, current_user_id(), 'attendance.history')));
-- resolving needs its own key even for a person who is also a member of the instance
CREATE POLICY follow_up_items_acting_resolve ON follow_up_items AS RESTRICTIVE FOR UPDATE
  USING (acting_hub() IS NULL OR actor_has_permission(tenant_id, current_user_id(), 'followup.resolve'));
-- a delegate never inserts a closure or an item: closing goes through the write path (system session)
CREATE POLICY conversation_closures_acting_no_insert ON conversation_closures AS RESTRICTIVE FOR INSERT
  WITH CHECK (acting_hub() IS NULL);
CREATE POLICY follow_up_items_acting_no_insert ON follow_up_items AS RESTRICTIVE FOR INSERT
  WITH CHECK (acting_hub() IS NULL);
