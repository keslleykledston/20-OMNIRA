-- Owner's default (2026-10-13): every attendant is available 24x7 and is, by default, a member of "Suporte", which is also the company's
-- default overflow queue (the last-resort destination of the overflow chain, ADR-0044). Each case is adjusted afterwards (shift, on-call,
-- sector) without touching this default. "Available 24x7" needs no row: an attendant with no shift calendar is unrestricted.
SET LOCAL lock_timeout = '5s';

ALTER TABLE tenants ADD COLUMN default_overflow_queue_id UUID;
ALTER TABLE tenants
  ADD CONSTRAINT tenants_default_overflow_queue_fk FOREIGN KEY (id, default_overflow_queue_id) REFERENCES queues (tenant_id, id) ON DELETE RESTRICT;

-- One definition of the default, used by this migration (every existing company) and by the company-creation code (new ones).
-- Idempotent: it only ADDS what is missing and never changes a queue's mode, a name the company already uses, or a member's settings.
CREATE OR REPLACE FUNCTION seed_tenant_sector_defaults(p_tenant UUID) RETURNS VOID AS $$
DECLARE
  v_support UUID;
  v_cal UUID;
  v_sector UUID;
BEGIN
  -- the three standard groups and their sectors (migration 123/124 did this for existing companies; new ones need it here)
  INSERT INTO queues (tenant_id, name, mode, is_default)
    SELECT p_tenant, s.name, 'manual', false FROM (VALUES ('Comercial'), ('Financeiro'), ('Suporte')) AS s(name)
    ON CONFLICT (tenant_id, name) DO NOTHING;
  INSERT INTO sectors (tenant_id, name)
    SELECT p_tenant, s.name FROM (VALUES ('Comercial'), ('Financeiro'), ('Suporte')) AS s(name)
    ON CONFLICT (tenant_id, name) DO NOTHING;
  UPDATE queues q SET sector_id = sc.id
    FROM sectors sc
    WHERE q.tenant_id = p_tenant AND sc.tenant_id = p_tenant AND sc.name = q.name AND q.name IN ('Comercial', 'Financeiro', 'Suporte')
      AND NOT q.is_default AND q.sector_id IS NULL;

  SELECT id INTO v_support FROM queues WHERE tenant_id = p_tenant AND name = 'Suporte';
  SELECT id INTO v_sector FROM sectors WHERE tenant_id = p_tenant AND name = 'Suporte';

  -- Suporte works 24x7 unless the company already gave it a calendar
  SELECT id INTO v_cal FROM calendars WHERE tenant_id = p_tenant AND name = '24x7 (padrão)';
  IF v_cal IS NULL THEN
    INSERT INTO calendars (tenant_id, name, timezone, kind, weekly) VALUES (p_tenant, '24x7 (padrão)', 'America/Manaus', '24x7', '[]'::jsonb) RETURNING id INTO v_cal;
  END IF;
  UPDATE sectors SET calendar_id = v_cal, updated_at = now() WHERE tenant_id = p_tenant AND id = v_sector AND calendar_id IS NULL;

  -- Suporte is the default overflow queue (unless the company already chose another)
  UPDATE tenants SET default_overflow_queue_id = v_support, updated_at = now() WHERE id = p_tenant AND default_overflow_queue_id IS NULL;

  -- every active attendant is, by default, a member of Suporte: available, capacity 5 (the first thing to tune per person)
  INSERT INTO queue_members (tenant_id, queue_id, user_id, active, available, capacity)
    SELECT m.tenant_id, v_support, m.user_id, true, true, 5
    FROM agent_profiles ap JOIN memberships m ON m.tenant_id = ap.tenant_id AND m.id = ap.membership_id
    WHERE ap.tenant_id = p_tenant AND ap.status = 'active' AND m.status = 'active'
    ON CONFLICT (tenant_id, queue_id, user_id) DO NOTHING;
END;
$$ LANGUAGE plpgsql SET search_path = pg_catalog, public, pg_temp;
REVOKE EXECUTE ON FUNCTION seed_tenant_sector_defaults(UUID) FROM PUBLIC;
GRANT EXECUTE ON FUNCTION seed_tenant_sector_defaults(UUID) TO omnira_app;

DO $$
DECLARE t UUID;
BEGIN
  FOR t IN SELECT id FROM tenants LOOP
    PERFORM seed_tenant_sector_defaults(t);
  END LOOP;
END $$;
