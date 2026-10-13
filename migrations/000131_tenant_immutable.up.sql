-- The write policies of the tables added by ADR-0044 check the tenant of the OLD row (USING) and of the NEW row (WITH CHECK) separately, so
-- a person who manages agents in two companies could move a row from one to the other with a single UPDATE. A row never changes company:
-- the trigger refuses it for every writer, owner role included.
CREATE OR REPLACE FUNCTION forbid_tenant_change() RETURNS trigger AS $$
BEGIN
  IF NEW.tenant_id IS DISTINCT FROM OLD.tenant_id THEN
    RAISE EXCEPTION 'a row cannot change company (% on %)', TG_OP, TG_TABLE_NAME USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['calendars', 'calendar_exceptions', 'calendar_holidays', 'calendar_suspensions', 'sectors', 'agent_shifts', 'on_call_rosters', 'on_call_periods',
                           'routing_engine_state', 'routing_engine_decisions', 'routing_engine_timers', 'overflow_policies', 'overflow_steps', 'sla_policies', 'conversation_sla'] LOOP
    EXECUTE format('CREATE TRIGGER %I BEFORE UPDATE OF tenant_id ON %I FOR EACH ROW EXECUTE FUNCTION forbid_tenant_change()', t || '_tenant_immutable', t);
  END LOOP;
END $$;
