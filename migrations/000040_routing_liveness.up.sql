-- IAM4.2-B0: durable routing-liveness state. JetStream delivery/retry (5s
-- NakWithDelay, MaxDeliver=10) is not durable routing state — it only covers
-- ~50s per conversation and then gives up silently, with no re-trigger
-- mechanism anywhere else in the system. routing_retry_at is the durable
-- fact "this conversation is due for another routing attempt" and is what
-- the safety sweep and presence-online wakeup (internal/worker/routing) use
-- to decide what to retrigger, and when it is safe to do so again (progress
-- guarantee: every retrigger bumps this forward, so a bounded batch cannot
-- starve on the same rows forever).
--
-- (routing_require_presence, originally planned as 000040, moves to 000041
-- when IAM4.2-B1 is implemented — see docs/adr/0010-agent-presence-and-heartbeat.md.)
ALTER TABLE conversations ADD COLUMN routing_retry_at TIMESTAMPTZ;

-- Supports the sweep/wakeup query directly: WHERE assigned_to_user_id IS NULL
-- AND status='open' AND queue_id IS NOT NULL AND routing_retry_at <= now(),
-- joined to queues(mode='round_robin'). The partial predicate keeps this
-- index tiny — it only ever covers conversations genuinely still needing
-- automated routing, never the whole table.
CREATE INDEX idx_conversations_routing_retry ON conversations(queue_id, routing_retry_at)
  WHERE assigned_to_user_id IS NULL AND status = 'open';
