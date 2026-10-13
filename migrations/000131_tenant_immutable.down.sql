DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['calendars', 'calendar_exceptions', 'calendar_holidays', 'calendar_suspensions', 'sectors', 'agent_shifts', 'on_call_rosters', 'on_call_periods',
                           'routing_engine_state', 'routing_engine_decisions', 'routing_engine_timers', 'overflow_policies', 'overflow_steps', 'sla_policies', 'conversation_sla'] LOOP
    EXECUTE format('DROP TRIGGER IF EXISTS %I ON %I', t || '_tenant_immutable', t);
  END LOOP;
END $$;
DROP FUNCTION IF EXISTS forbid_tenant_change();
