-- M04.4: only the trusted worker system session may advance outbox state.
CREATE POLICY outbox_events_update_system ON outbox_events
  FOR UPDATE
  USING (is_system_admin())
  WITH CHECK (is_system_admin());
