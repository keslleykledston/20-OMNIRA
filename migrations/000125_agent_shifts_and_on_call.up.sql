-- ADR-0044 phase 3 (cont.): the agent's own shift and on-call/contingency rosters. Additive: an agent with no shift calendar and nobody
-- on call behave exactly as before. Schedule of the sector, shift of the agent, presence and capacity stay four separate things.
SET LOCAL lock_timeout = '5s';

-- the agent's shift is a calendar (the same domain type as the sector's). Scope: one queue, one sector, or (both NULL) everything the
-- agent does in the tenant. The most specific wins: queue > sector > all.
CREATE TABLE agent_shifts (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  calendar_id UUID NOT NULL,
  queue_id UUID,
  sector_id UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (NOT (queue_id IS NOT NULL AND sector_id IS NOT NULL)),
  UNIQUE NULLS NOT DISTINCT (tenant_id, user_id, queue_id, sector_id),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE RESTRICT,
  FOREIGN KEY (tenant_id, queue_id) REFERENCES queues (tenant_id, id) ON DELETE CASCADE,
  FOREIGN KEY (tenant_id, sector_id) REFERENCES sectors (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_agent_shifts_user ON agent_shifts (tenant_id, user_id);

-- on-call and contingency teams. A roster may be tied to one sector (NULL = the whole tenant).
CREATE TABLE on_call_rosters (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  kind TEXT NOT NULL CHECK (kind IN ('on_call', 'contingency')),
  sector_id UUID,
  active BOOLEAN NOT NULL DEFAULT true,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name),
  FOREIGN KEY (tenant_id, sector_id) REFERENCES sectors (tenant_id, id) ON DELETE RESTRICT
);

CREATE TABLE on_call_periods (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  roster_id UUID NOT NULL,
  user_id UUID NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  starts_at TIMESTAMPTZ NOT NULL,
  ends_at TIMESTAMPTZ NOT NULL,
  capacity INTEGER NOT NULL DEFAULT 1 CHECK (capacity BETWEEN 1 AND 100),
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at AND ends_at - starts_at <= interval '31 days'),
  FOREIGN KEY (tenant_id, roster_id) REFERENCES on_call_rosters (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_on_call_periods_active ON on_call_periods (tenant_id, roster_id, starts_at, ends_at);
CREATE INDEX idx_on_call_periods_user ON on_call_periods (tenant_id, user_id, ends_at);

-- The database itself refuses to schedule somebody who has no ACTIVE membership in the tenant (user_id references the global users table, and
-- RLS only looks at tenant_id): a Hub operator, a person of another company or a disabled member can never get a shift or an on-call period by any writer.
CREATE OR REPLACE FUNCTION scheduling_user_is_member() RETURNS trigger AS $$
BEGIN
  IF NOT EXISTS (SELECT 1 FROM public.memberships m WHERE m.tenant_id = NEW.tenant_id AND m.user_id = NEW.user_id AND m.status = 'active') THEN
    RAISE EXCEPTION 'user % has no active membership in tenant %', NEW.user_id, NEW.tenant_id USING ERRCODE = '23514';
  END IF;
  RETURN NEW;
END;
$$ LANGUAGE plpgsql SECURITY DEFINER SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION scheduling_user_is_member() FROM PUBLIC;
CREATE TRIGGER agent_shifts_member_guard BEFORE INSERT OR UPDATE OF tenant_id, user_id ON agent_shifts FOR EACH ROW EXECUTE FUNCTION scheduling_user_is_member();
CREATE TRIGGER on_call_periods_member_guard BEFORE INSERT OR UPDATE OF tenant_id, user_id ON on_call_periods FOR EACH ROW EXECUTE FUNCTION scheduling_user_is_member();

DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['agent_shifts', 'on_call_rosters', 'on_call_periods'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_delete_tenant', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;
