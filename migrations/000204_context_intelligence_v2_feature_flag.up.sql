-- CONTEXT.2 operator kill switch, same pattern as ADR-0058's group_sla_alert_feature (migration
-- 000184): a platform operator's admin-console button that PAUSES/RESUMES the window analysis
-- runner (CONTEXT.2A/2B) without a worker restart. This is a SECOND, finer-grained gate — it does
-- NOT replace OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED, which stays the host-level switch deciding
-- whether the CONTEXT.2 subsystem is wired up at all (FOUNDATION CODE, not cleared for production;
-- docs/implementation/CONTEXT-INTELLIGENCE-V2.md). Both default to false, and the env var is never
-- set to true by this migration or any automated process.
--
-- No tenant_id — platform-wide, independent of any tenant's own topic/intelligence flags. RLS
-- follows the exact same manual pattern as platform_operators (migration 000099) and
-- group_sla_alert_feature (migration 000184): FORCE ROW LEVEL SECURITY, restricted to
-- is_system_admin() OR is_platform_operator(current_user_id()).
CREATE TABLE context_intelligence_v2_feature (
  singleton  BOOLEAN NOT NULL DEFAULT true PRIMARY KEY,
  enabled    BOOLEAN NOT NULL DEFAULT false,
  updated_by UUID REFERENCES users(id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (singleton)
);
INSERT INTO context_intelligence_v2_feature DEFAULT VALUES;

ALTER TABLE context_intelligence_v2_feature ENABLE ROW LEVEL SECURITY;
ALTER TABLE context_intelligence_v2_feature FORCE ROW LEVEL SECURITY;

CREATE POLICY context_intelligence_v2_feature_platform_operator_only ON context_intelligence_v2_feature FOR ALL
  USING (is_system_admin() OR is_platform_operator(current_user_id()))
  WITH CHECK (is_system_admin() OR is_platform_operator(current_user_id()));

GRANT SELECT, INSERT, UPDATE, DELETE ON context_intelligence_v2_feature TO omnira_app;
