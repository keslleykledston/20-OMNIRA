# ADR-0010: Agent Presence and Heartbeat Architecture

**Date:** 2026-09-22

**Status:** Accepted. IAM4.2-A implemented and committed 2026-09-22 (`9b5bb00`) — see `docs/delivery/HANDOFF-NEXT-AGENT.md` § IAM4.2-A. §11-12 enforcement (IAM4.2-B) is design-approved but **blocked on a routing-liveness prerequisite** (§16 below) — not implemented, routing untouched.

## 16. Addendum — IAM4.2-B design (2026-09-22)

Presence-enforcement design (chunked candidate pagination, `agent_profile_id` as the sole presence identifier, `tenants.routing_require_presence` flag, `ErrPresenceUnavailable` as a distinct transient error) is human-approved. Full delta not duplicated here — see `docs/delivery/IAM4.2-PRESENCE-DESIGN-GATE.md` § IAM4.2-B for the complete design and the routing-liveness finding that blocks turning the flag on for any tenant.

**Blocking finding:** `job.routing.assign.v1` is published exactly once per conversation (`internal/inbox/adapters/postgres.go` `RouteNew`), and the JetStream consumer gives up silently after `MaxDeliver:10` (~50s of `NakWithDelay(5s)`) with no re-trigger mechanism anywhere in the repo. This is a **pre-existing IAM4.1 gap** (today's `ErrNoEligibleAgent` can already strand a conversation this way), but IAM4.2-B's fail-closed `ErrPresenceUnavailable` on Valkey outages makes hitting it far more likely and consequential. **IAM4.2-B1 (the enforcement flag) may not be enabled for any tenant until IAM4.2-B0 (routing liveness: a presence-online wakeup + a slow bounded safety sweep, both reusing the existing outbox/NATS/consumer pipeline unchanged) ships first.**

**IAM4.2-B0 status (2026-09-22): DONE.** Human gate approved; committed as `fix(routing): retrigger stale unassigned conversations`. Durable retry state (`conversations.routing_retry_at`, migration `000040`) plus a presence-online wakeup and a slow bounded safety sweep (`internal/worker/routing.Sweep`/`.PresenceWakeup`), both re-enqueueing `job.routing.assign.v1` through the unmodified outbox/NATS/consumer/`AssignRoundRobin` pipeline. `MaxDeliver`/backoff untouched; no presence-enforcement code (`routing_require_presence`, Valkey lookups in `AssignRoundRobin`, `ErrPresenceUnavailable`) written — that remains entirely IAM4.2-B1, now unblocked. **Migration ownership:** `000040` = routing liveness (B0); `000041` = presence enforcement flag (B1, not yet created).

**Context:** IAM4.2 introduces agent presence — the ability to know if an agent is currently active and eligible to receive work. This is distinct from `queue_members.available`, which is administrative eligibility per queue. Presence must be transient, realtime and integrated with existing SSE infrastructure (NATS bridge).

**Principles (Frozen from IAM4.1):**
- Membership = identity/access (tenant)
- AgentProfile = operational participation (status: active|disabled)
- queue_members.available = queue-local routing eligibility
- Valkey was deferred pending a demonstrated cache/presence need — presence is that need (per `docs/adr/0006`).

---

## Decision (human-approved 2026-09-22)

Presence is an **ephemeral realtime state living in Valkey**, not a Postgres-write-per-heartbeat model. Postgres persists only the durable, coalesced `last_seen_at`. Routing does not depend on presence in the first rollout.

### 1. Presence model

- States: **`online` | `offline`** only. No `away`/`busy` in IAM4.2.

### 2. Heartbeat

- Client sends heartbeat every **30 seconds** while the SSE connection is open.
- Endpoint is **self-scoped**: `POST /me/presence/heartbeat`.
  - Tenant, membership and AgentProfile are resolved from the authenticated session.
  - Client never supplies `agent_id`/`tenant_id` in the payload.

### 3. TTL

- **120 seconds** initially (not 5 minutes). Two missed heartbeats (30s cadence) tolerate one dropped beat before flipping offline.

### 3.1 Expiry mechanism (technical correction, added 2026-09-22 before IAM4.2-A implementation)

- The TTL on a Valkey key/score governs session **validity**, but online→offline detection does **not** rely on Valkey/Redis keyspace-notification events (`notify-keyspace-events`) as the mechanism of record. Keyspace notifications are opt-in server config, delivered best-effort over pub/sub, and silently dropped if a subscriber is briefly down — unacceptable as the sole trigger for a state transition that also drives a coalesced Postgres write and a NATS event.
- The primary mechanism is a **deterministic reaper**: a periodic process that queries a bounded, score-sorted expiry index (never a full keyspace scan) for sessions past their TTL, retires them, and computes the resulting online/offline transition from the authoritative per-agent session count — see `internal/presence/application.Reaper` and `internal/presence/adapters.Store.ExpireBatch`.
- If keyspace notifications are added later, they may only ever be an **optimization** (lower latency detection) layered on top of the reaper — never a replacement for it. The reaper must keep running regardless.

### 4. Source of truth (realtime)

- **Valkey**, keyed per session/tab, with native TTL/EXPIRE.
- Postgres does **not** receive a write per heartbeat and is never read for the realtime online/offline decision.

### 5. Postgres role

- Durable persistence only: `last_seen_at` on the agent's durable record, written **coalesced** (batched/periodic), never on every 30s heartbeat.
- No `agent_presence` realtime table in Postgres. Postgres is a checkpoint of history, not the operational source.

### 6. Multiple tabs/sessions

- Presence is tracked **per session/tab**, each with its own TTL key in Valkey.
- **Agent is online iff at least one session is alive.** Closing one tab must not flip the agent offline while another session's heartbeat is still live.

### 7. Heartbeat API shape

- `POST /me/presence/heartbeat` — self-scoped, session-authenticated. No arbitrary `agent_id` selection by the client.

### 8. NATS

- Do **not** publish every heartbeat.
- Publish only aggregated **state transitions**: `offline → online` and `online → offline` (per agent, not per session).

### 9. SSE / Supervisor delivery

- Supervisor flow: **GET snapshot** (initial state) **+ SSE incremental** (transition events).
- Polling is a **fallback only**, not the primary path.

### 10. last_seen semantics

- Persisted to Postgres **coalesced** — not on every heartbeat (e.g., batched write, or on transition, or periodic flush). Never a 30s write cadence to Postgres.

### 11. Routing (deferred activation)

- Final eligibility rule (once enforced):
  `active membership + active AgentProfile + queue availability + capacity + online presence`
- **Not activated as a hard requirement on first deploy.** Routing in IAM4.2-A continues to operate exactly as IAM4.1 (presence is observed, not enforced).

### 12. Rollout plan

**IAM4.2-A** (this slice):
- Heartbeat endpoint
- Valkey presence store (per-session TTL)
- Coalesced `last_seen_at` persistence in Postgres
- NATS transition events (aggregated online/offline)
- SSE presence stream (snapshot + incremental) for supervisors, read-only
- Routing is **unchanged** — does not consult presence

**IAM4.2-B** (follow-up, gated separately):
- Enable presence as a routing eligibility requirement
- Rollout **first on the pilot tenant**, controlled and reversible

### 13. Failure semantics (post-enforcement, IAM4.2-B only)

- If Valkey/presence is unavailable **after enforcement is enabled**: automated routing (round-robin) **fails closed** (no eligible agent found, rather than silently ignoring presence).
- Manual claim/assign are unaffected and continue under existing IAM4.1 rules regardless of presence availability.

### 14. Deferred (explicitly out of scope)

- `away` / `busy` states
- Skills
- Performance metrics
- Presence analytics/historical trends

### 15. Auditing

- Individual heartbeats are **not audited**. Only aggregated state transitions may be observed via NATS/SSE; no per-heartbeat audit log entries.

---

## Rejected Alternative

**Postgres-backed presence with SSE heartbeat (write-per-heartbeat to Postgres, Postgres as source of truth)** — rejected by human decision. Conflicts with presence being explicitly ephemeral; a 30s write cadence to Postgres was judged unnecessary load and the wrong source-of-truth model for a transient signal. Valkey is adopted specifically to serve this need, consistent with `docs/adr/0006` (Valkey enters when cache/presence is actually required).

---

## Related Decisions

- **ADR-0001:** Tenant isolation applies to presence data (session/tenant resolution server-side only)
- **ADR-0006:** Postgres is source of truth for durable data; Valkey is the ephemeral/cache layer — presence is the first real use case triggering Valkey adoption
- **ADR-0008:** Go backend implements heartbeat/session resolution; frontend is the SSE + heartbeat client
- **IAM4.1 frozen:** AgentProfile and queue_members.available unchanged; presence is additive and, in IAM4.2-A, non-blocking

---

## References

- `docs/delivery/IAM4.2-PRESENCE-DESIGN-GATE.md` — design exploration and superseded proposals
- `docs/adr/0006-nats-jetstream-and-postgres-source-of-truth.md` — Postgres-first / Valkey-deferred principle
- `internal/worker/realtime/bridge.go` — existing Postgres NOTIFY → NATS → SSE infrastructure (pattern reused for transition events)
- `docs/delivery/IAM4-AGENT-MANAGEMENT.md` — IAM4.1 decisions
- `docs/delivery/HANDOFF-NEXT-AGENT.md` — canonical handoff, IAM4.2 rollout status
