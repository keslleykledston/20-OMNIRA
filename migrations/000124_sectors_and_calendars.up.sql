-- ADR-0044 phase 3: sectors (a sector may own several queues) and operational calendars. Additive: a queue with no sector and no
-- calendar behaves exactly as before. Everything is per tenant, composite foreign keys keep references inside the tenant.

CREATE TABLE calendars (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 80),
  timezone TEXT NOT NULL CHECK (char_length(timezone) BETWEEN 1 AND 64), -- IANA name, validated by the application (internal/scheduling)
  kind TEXT NOT NULL CHECK (kind IN ('24x7', 'business', 'custom')),
  weekly JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(weekly) = 'array' AND octet_length(weekly::text) <= 16384),
  revision INTEGER NOT NULL DEFAULT 1 CHECK (revision >= 1), -- bumped by every change: timers derive their due times from it
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name)
);

CREATE TABLE calendar_exceptions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  calendar_id UUID NOT NULL,
  name TEXT NOT NULL DEFAULT '' CHECK (char_length(name) <= 80),
  from_date DATE NOT NULL,
  to_date DATE NOT NULL,
  closed BOOLEAN NOT NULL,
  intervals JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(intervals) = 'array' AND octet_length(intervals::text) <= 4096),
  CHECK (to_date >= from_date),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_calendar_exceptions_calendar ON calendar_exceptions (tenant_id, calendar_id, from_date);

CREATE TABLE calendar_holidays (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  calendar_id UUID NOT NULL,
  name TEXT NOT NULL DEFAULT '' CHECK (char_length(name) <= 80),
  on_date DATE NOT NULL,
  intervals JSONB NOT NULL DEFAULT '[]'::jsonb CHECK (jsonb_typeof(intervals) = 'array' AND octet_length(intervals::text) <= 4096),
  UNIQUE (calendar_id, on_date),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE CASCADE
);

CREATE TABLE calendar_suspensions (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  calendar_id UUID NOT NULL,
  starts_at TIMESTAMPTZ NOT NULL,
  ends_at TIMESTAMPTZ NOT NULL,
  reason TEXT NOT NULL DEFAULT '' CHECK (char_length(reason) <= 200),
  created_by UUID REFERENCES users(id) ON DELETE SET NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (ends_at > starts_at),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE CASCADE
);
CREATE INDEX idx_calendar_suspensions_calendar ON calendar_suspensions (tenant_id, calendar_id, ends_at);

CREATE TABLE sectors (
  id UUID PRIMARY KEY DEFAULT uuid_generate_v4(),
  tenant_id UUID NOT NULL REFERENCES tenants(id) ON DELETE CASCADE,
  name TEXT NOT NULL CHECK (char_length(name) BETWEEN 1 AND 60),
  active BOOLEAN NOT NULL DEFAULT true, -- deactivating never deletes: flows keep the stable id and take the "unavailable" port
  calendar_id UUID,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (tenant_id, id),
  UNIQUE (tenant_id, name),
  FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE RESTRICT
);

-- inheritance: queue -> sector -> tenant default -> none (always open, the behaviour before this migration)
ALTER TABLE queues ADD COLUMN sector_id UUID, ADD COLUMN calendar_id UUID;
ALTER TABLE queues
  ADD CONSTRAINT queues_sector_fk FOREIGN KEY (tenant_id, sector_id) REFERENCES sectors (tenant_id, id) ON DELETE RESTRICT,
  ADD CONSTRAINT queues_calendar_fk FOREIGN KEY (tenant_id, calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE RESTRICT;
CREATE INDEX idx_queues_sector ON queues (tenant_id, sector_id) WHERE sector_id IS NOT NULL;
ALTER TABLE tenants ADD COLUMN default_calendar_id UUID;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_default_calendar_fk FOREIGN KEY (id, default_calendar_id) REFERENCES calendars (tenant_id, id) ON DELETE RESTRICT;

-- RLS: same model as queues (membership reads and writes; the permission, agent.read / agent.manage, is checked by the handlers)
DO $$
DECLARE t TEXT;
BEGIN
  FOREACH t IN ARRAY ARRAY['calendars', 'calendar_exceptions', 'calendar_holidays', 'calendar_suspensions', 'sectors'] LOOP
    EXECUTE format('ALTER TABLE %I ENABLE ROW LEVEL SECURITY', t);
    EXECUTE format('ALTER TABLE %I FORCE ROW LEVEL SECURITY', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR SELECT USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_read_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR INSERT WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_insert_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR UPDATE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin()) WITH CHECK (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_update_tenant', t);
    EXECUTE format('CREATE POLICY %I ON %I FOR DELETE USING (has_active_membership(tenant_id, current_user_id()) OR is_system_admin())', t || '_delete_tenant', t);
    EXECUTE format('GRANT SELECT, INSERT, UPDATE, DELETE ON %I TO omnira_app', t);
  END LOOP;
END $$;

-- data: the standard groups of migration 123 get a sector of the same name and are linked to it (idempotent; the Default queue stays without)
INSERT INTO sectors (tenant_id, name)
SELECT DISTINCT q.tenant_id, q.name FROM queues q
WHERE q.name IN ('Comercial', 'Financeiro', 'Suporte') AND NOT q.is_default AND q.sector_id IS NULL
ON CONFLICT (tenant_id, name) DO NOTHING;
UPDATE queues q SET sector_id = s.id
FROM sectors s
WHERE s.tenant_id = q.tenant_id AND s.name = q.name AND q.name IN ('Comercial', 'Financeiro', 'Suporte') AND NOT q.is_default AND q.sector_id IS NULL;
