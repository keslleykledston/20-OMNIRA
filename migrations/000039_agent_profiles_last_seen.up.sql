-- IAM4.2-A: durable checkpoint of presence activity. Written coalesced by the
-- background flush loop (internal/presence), never per heartbeat. Not a
-- realtime presence source: online/offline is decided by Valkey only
-- (ADR-0010).
ALTER TABLE agent_profiles ADD COLUMN last_seen_at TIMESTAMPTZ;
