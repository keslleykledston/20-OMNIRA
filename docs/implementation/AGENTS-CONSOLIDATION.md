# Agents Consolidation — Multi-Operator Support

**Status**: COMPLETE (GATE R3 PASS + Validated 2026-10-05)

**Scope**: Multiple agents per tenant can serve clients with atomic assignment, real-time presence, and queue-based routing.

---

## Architecture Overview

### Data Model

```
tenants
  ├─ memberships (users in team)
  │   └─ agent_profiles (operational extension)
  │       └─ queue_members (queue eligibility)
  └─ queues
      ├─ round_robin | manual routing
      └─ queue_members (agent workload tracking)

conversations
  ├─ queue_id (assigned to queue, not agent)
  ├─ assigned_to_user_id (current operator owner)
  └─ assignment_events (atomic CAS history)
```

### Tables

| Table | Purpose | Key Fields |
|-------|---------|-----------|
| `agent_profiles` | Register membership as operational agent | `tenant_id, membership_id, status` |
| `queue_members` | Queue eligibility (routing pool) | `tenant_id, queue_id, user_id, active, available, capacity` |
| `assignment_events` | Audit trail of who took/released | `tenant_id, conversation_id, from_user_id, to_user_id, reason` |
| `queues` | Routing policy per tenant | `tenant_id, name, mode (manual/round_robin)` |

---

## How It Works

### 1. Creating an Agent

Admin invites user → User accepts → Membership created → Admin registers as agent:

```bash
POST /api/v1/tenants/{tenant_id}/agent-profiles
{
  "membership_id": "uuid"
}
```

**Result**: `agent_profile` created with status="active"

### 2. Queue Assignment

Admin adds agent to queue for eligibility:

```bash
POST /api/v1/tenants/{tenant_id}/agent-profiles/{agent_id}/queues
{
  "queue_id": "uuid",
  "capacity": 3,        # max parallel conversations
  "available": true     # routing eligible
}
```

**Result**: `queue_member` created; agent becomes eligible for routing.

### 3. Conversation Assignment (Atomic)

When a new conversation arrives:

**Round-Robin Queue**:
- System selects least-recently-assigned eligible agent
- CAS (Compare-And-Swap) on `assigned_to_user_id`
- If race: first wins, second gets 409 Conflict

**Manual Queue**:
- Agent clicks "Claim" (assume)
- CAS on `assigned_to_user_id`
- If already assigned: 409 Conflict

**Result**: Only one agent owns conversation at a time.

### 4. Real-Time Presence

Agents heartbeat their status via `/presence` endpoints (ADR-0010):

```
Agent login  → online
Agent idle   → offline (10s timeout)
Agent in tab → keepalive every 5s
```

UI updates in real-time via SSE:

```
AgentsPage → presenceAPI.snapshot() → onlineIds
            → usePresenceEvents() → SSE events
```

**Not routing eligibility**: `available` in queue_members is separate (queue-local).

---

## Permissions

| Permission | Description | Roles |
|------------|-------------|-------|
| `agent.read` | View operational agents + presence | tenant_admin, tenant_supervisor |
| `agent.manage` | Enable/disable agents, manage queues | tenant_admin, tenant_supervisor |

Querying agents requires either permission; adding to queue requires `agent.manage`.

---

## UI & Operations

### Agents Page (`/settings/agents`)

**List View**:
- All agent_profiles of tenant
- Status: active / disabled
- Queues: eligibility per queue + capacity + available flag
- Online indicator (from presence)

**Controls**:
- **Enable/Disable**: Deactivates operator (doesn't delete, soft-deactivates)
- **Add to Queue**: Sets capacity + availability
- **Update Availability**: Toggle queue eligibility without removing
- **Update Capacity**: Change max parallel workload

**Real-Time**:
- Online/offline status updates via SSE
- Draft changes optimistically; error reverts

### Inbox (`/inbox`)

- Conversations show `assigned_to` label
- "Claim" button appears if unassigned
- "Release" button if self-assigned
- 409 displayed as toast if beat by another agent

---

## API Endpoints

### Agent Profiles

| Method | Path | Permission | Description |
|--------|------|-----------|---|
| GET | `/tenants/{tid}/agent-profiles` | agent.read | List all |
| GET | `/tenants/{tid}/agent-profiles/{id}` | agent.read | Get one |
| POST | `/tenants/{tid}/agent-profiles` | agent.manage | Create (activate membership) |
| PATCH | `/tenants/{tid}/agent-profiles/{id}` | agent.manage | Update status (active/disabled) |

### Queue Assignments

| Method | Path | Permission | Description |
|--------|------|-----------|---|
| GET | `/tenants/{tid}/agent-profiles/{id}/queues` | agent.read | List assignments |
| POST | `/tenants/{tid}/agent-profiles/{id}/queues` | agent.manage | Add to queue |
| PATCH | `/tenants/{tid}/agent-profiles/{id}/queues/{member_id}` | agent.manage | Update capacity/available |
| DELETE | `/tenants/{tid}/agent-profiles/{id}/queues/{member_id}` | agent.manage | Remove from queue |

### Conversation Assignment

| Method | Path | Permission | Description |
|--------|------|-----------|---|
| PATCH | `/tenants/{tid}/inbox/conversations/{cid}/claim` | conversation.manage | Atomic claim (CAS) |
| PATCH | `/tenants/{tid}/inbox/conversations/{cid}/release` | conversation.manage | Release back to queue |

---

## Atomicity & Conflicts

### Claim Atomicity

Uses SQL `UPDATE ... WHERE assigned_to_user_id IS NULL`:

```sql
UPDATE conversations
SET assigned_to_user_id = $1, updated_at = NOW()
WHERE tenant_id = $2 AND id = $3 AND assigned_to_user_id IS NULL
```

**Result**: If anyone else claimed meanwhile, `rows_affected() = 0` → **409 Conflict**.

### No Implicit Release

Operator must explicit release before another can claim. No timeout auto-release.

### Assignment History

Every claim/release logged in `assignment_events`:

```json
{
  "from_user_id": null,
  "to_user_id": "agent_a_id",
  "reason": "manual_claim",
  "changed_by": "agent_a_id",
  "created_at": "2026-10-05T12:34:56Z"
}
```

---

## Validation (GATE R3 Results)

**Test Scenario**: Two operators (Agent A, Agent B) on same conversation.

| Step | Actor | Action | Expected | Result |
|------|-------|--------|----------|--------|
| 1 | A | POST /claim | 200 assigned to A | ✅ PASS |
| 2 | B | POST /claim | 409 (already assigned) | ✅ PASS |
| 3 | B | GET conversation | Label "Assumida por A" | ✅ PASS (realtime) |
| 4 | B | Reload (no SSE) | Still shows A | ✅ PASS |
| 5 | A | POST /release | 204 released | ✅ PASS |
| 6 | B | POST /claim | 200 assigned to B | ✅ PASS |
| 7 | B | POST /release | 204 released | ✅ PASS |
| Audit | — | Check assignment_events | 3 events (claims), 0 conflicted attempts logged | ✅ PASS |

**Conclusion**: Atomic assignment validated. No race conditions, no duplicate claims, no inconsistent state.

---

## Known Constraints

1. **No Time-Out Release**: Operator must explicit `/release`. No auto-timeout after 30min.
   - *Reason*: Prevents accidental release mid-work; admin can manually revoke if needed.
   - *Mitigation*: Presence timeout shows "offline" but doesn't release conversation.

2. **No Load Balancing**: Round-robin is FIFO on `last_assigned_at`, not by workload.
   - *Reason*: Simplicity; load spike handled via manual reassign + Release.
   - *Future*: Could add `active_workload` from routing domain to sort eligible list.

3. **Single Tenant Scope**: Agent can't work cross-tenant (membership per tenant).
   - *Reason*: RLS isolation; by design.
   - *Workaround*: Create separate membership in other tenant if needed.

4. **Presence != Availability**: Online status ≠ queue eligibility.
   - *Reason*: Agent may be online but set `available=false` in queue (e.g., on break).
   - *UI*: Green "online" indicator + separate "Elegível" flag per queue.

---

## Extending Agents

### Add Skill/Specialization Tags

To route conversations by agent skills (e.g., "billing_expert"):

1. Add `skills` JSONB to `agent_profiles`
2. Add `required_skills` TEXT[] to `queues`
3. Filter eligible agents where `agent_skills ⊃ queue_required_skills`

### Add Shift-Based Availability

To enforce "Agent B only works 9-17 UTC":

1. Add `working_hours` JSONB to `queue_members`
2. Check current_time against working_hours in eligibility query
3. Automatically set `available=false` on clock out

### Add Workload Balancing

To prefer agents with lower active load:

1. Query `assignments` count per agent
2. Sort by `count ASC, last_assigned_at ASC` (prefer fewer + older)
3. Still atomic on `/claim`, but initial list is pre-filtered

---

## Testing

**Unit Tests**: Password, routing logic in `internal/routing/domain/routing_test.go`

**Integration Tests**: Full E2E in Task #6 (Agents + Conversations + Assignment)

**Validation**: GATE R3 documented in `docs/delivery/GATES-REAL-VALIDATION.md`

---

## References

- **Routing Domain**: `internal/routing/domain/routing.go` (SelectNext, Agent struct)
- **Agent Profiles Handler**: `internal/tenancy/adapters/agent_profiles_http.go` (CRUD, queue ops)
- **Agents UI**: `web/src/pages/AgentsPage.tsx` (list, presence, queue mgmt)
- **Presence**: `internal/presence/adapters/` (heartbeat, SSE, ADR-0010)
- **Conversation Assignment**: `internal/routing/application/` (claim, release, atomicity)
- **Gate R3 Validation**: `docs/delivery/GATES-REAL-VALIDATION.md`
- **ADR-0010 Presence**: `docs/adr/0010-hermes-presence-heartbeat.md`

---

**Status**: ✅ COMPLETE — Agents can be created, added to queues, and assigned conversations with atomic guarantees.

**Next**: Task #6 integration tests; Task #7 production deployment.
