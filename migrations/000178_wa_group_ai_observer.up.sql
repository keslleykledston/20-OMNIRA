-- ADR-0055: per-group opt-in for the AI observer (suggest only, never act). Independent of `enabled` (reading)
-- and `purpose` — a group can read without the observer, but the observer never runs on a group that is not
-- reading (there would be nothing to observe).
ALTER TABLE wa_groups ADD COLUMN ai_observer_enabled BOOLEAN NOT NULL DEFAULT false;
ALTER TABLE wa_groups ADD COLUMN ai_observer_set_by UUID REFERENCES users(id) ON DELETE SET NULL;
ALTER TABLE wa_groups ADD COLUMN ai_observer_set_at TIMESTAMPTZ;
