-- ADR-0058 addendum: replaces OMNIRA_GROUP_SLA_ALERT_ENABLED (.env + worker restart) with a runtime
-- toggle a platform operator can flip from the admin console, no redeploy needed. Same singleton-row
-- shape as platform_installations (migration 000099/000173): the boolean PK + CHECK guarantees, in the
-- schema itself, that this table never holds a second row.
--
-- No tenant_id — this is a PLATFORM-WIDE kill switch, independent of any tenant's own is_alert_channel
-- choice (wa_groups, migration 000183). RLS follows the exact same manual pattern as platform_operators
-- (migration 000099): FORCE ROW LEVEL SECURITY, restricted to is_system_admin() OR
-- is_platform_operator(current_user_id()). The Go side (internal/groups/application.SLAAlertService)
-- checks this on every SLA-breach event — never only once at worker startup — so flipping it takes
-- effect immediately.
CREATE TABLE group_sla_alert_feature (
  singleton  BOOLEAN NOT NULL DEFAULT true PRIMARY KEY,
  enabled    BOOLEAN NOT NULL DEFAULT false,
  updated_by UUID REFERENCES users(id),
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  CHECK (singleton)
);
INSERT INTO group_sla_alert_feature DEFAULT VALUES;

ALTER TABLE group_sla_alert_feature ENABLE ROW LEVEL SECURITY;
ALTER TABLE group_sla_alert_feature FORCE ROW LEVEL SECURITY;

CREATE POLICY group_sla_alert_feature_platform_operator_only ON group_sla_alert_feature FOR ALL
  USING (is_system_admin() OR is_platform_operator(current_user_id()))
  WITH CHECK (is_system_admin() OR is_platform_operator(current_user_id()));

GRANT SELECT, INSERT, UPDATE, DELETE ON group_sla_alert_feature TO omnira_app;
