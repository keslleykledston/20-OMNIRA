-- Inbox: per-tenant thresholds (minutes) that colour the "customer is waiting"
-- chip in the conversation list: attention after wait_warn_minutes, critical
-- after wait_danger_minutes. Display only — these are not a contractual SLA and
-- nothing is enforced or escalated from them. Defaults match what the UI used
-- before this was configurable (30 min / 2 h), so existing tenants see no change.
-- Rides on the existing tenants RLS: any member reads, only an admin updates.
ALTER TABLE tenants
  ADD COLUMN wait_warn_minutes   INTEGER NOT NULL DEFAULT 30,
  ADD COLUMN wait_danger_minutes INTEGER NOT NULL DEFAULT 120,
  ADD CONSTRAINT tenants_wait_thresholds_chk
    CHECK (wait_warn_minutes >= 1 AND wait_danger_minutes > wait_warn_minutes AND wait_danger_minutes <= 10080);
