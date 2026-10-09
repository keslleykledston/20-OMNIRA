-- The hub audit view (ADR-0038 §6) reads the append-only audit_events by (hub, time) and by (instance, time). Without these, a hub with few
-- matching events would scan most of a large table to fill one page of 50 (Codex review). Partial: only the rows the view can ever return.
CREATE INDEX audit_events_hub_view_idx ON audit_events ((metadata ->> 'hub_id'), created_at DESC, id DESC) WHERE metadata ? 'hub_id';
CREATE INDEX audit_events_tenant_time_idx ON audit_events (tenant_id, created_at DESC, id DESC) WHERE tenant_id IS NOT NULL;
