-- IAM4.2-B1: per-tenant opt-in for presence-aware automated routing.
-- Default false preserves today's behavior exactly (zero Valkey coupling,
-- IAM4.1 eligibility only) for every existing and future tenant until
-- explicitly enabled per pilot (docs/adr/0010-agent-presence-and-heartbeat.md).
-- Applies only to automated round-robin (AssignRoundRobin); manual
-- claim/assign are never affected by this flag.
ALTER TABLE tenants ADD COLUMN routing_require_presence BOOLEAN NOT NULL DEFAULT false;
