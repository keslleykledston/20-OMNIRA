# OMNIRA Service Hub — Product Requirements Document

**Status**: Phase 1 (2026-10-07)  
**Baseline**: 6d36f67

## Executive Summary

Enable OMNIRA to support delegated operations through a Service Hub:

- **Multi-Tenant Agent Workspace**: A single unified inbox where an agent operates multiple tenants simultaneously without manual context switching.
- **Service Hub Delegation**: A tenant can delegate specific capabilities to a Service Hub, which dispatches work via a shared workforce.
- **Tenant Integration Gateway**: Isolate and manage all external integrations (ERP, CRM, OSS/BSS, etc.) with per-tenant credentials, capabilities, and idempotency.
- **Agent Performance**: Reduce triaging time, eliminate context switches, centralize next-best-action, ensure SLA adherence, prevent identity errors.

## Business Model

```
OMNIRA Tenant (e.g., ISP A)
  ├─ Direct agents (own staff)
  └─ Service Hub Delegation
      └─ Hub agent (K3G staff) via delegated service contract

Service Hub (e.g., K3G Service Desk)
  ├─ Hub membership
  ├─ Work pools (NOC N1, Support L2, etc.)
  ├─ Unified inbox (delegates all work)
  ├─ Routing engine (skills + capacity + continuity)
  ├─ Supervisor dashboard
  └─ Operations (Health, SLA, reconciliation)
```

## Non-Goals

- Transforming Hub agents into Tenant employees (no artificial membership)
- Global system-admin workarounds for Hub access
- Abandoning RLS or Row Level Security
- Microservices refactor
- Kubernetes without ADR
- New databases per Tenant (multi-schema or multi-database via separate tier only)

## Scope: This Epic

### 1. Hub Domain Model

Tables (new):
- `service_hubs` — hub metadata
- `hub_memberships` — users in hub
- `hub_tenant_service_contracts` — which tenants the hub serves, what permissions
- `work_pools` — pools of work (NOC, Support, Finance, etc.)
- `work_pool_members` — agents assigned to pool
- `skills` — skill library
- `agent_skills` — agent capabilities
- `effective_access_grants` — cached projection of what an agent can access

Schema:
- All tables inherit tenant isolation where applicable
- Hub membership is orthogonal to Tenant membership
- Service contract active/revoked status controls access

### 2. EffectiveTenantContext v2

Extend TenantContext to support:

```go
type EffectiveTenantContext struct {
    TenantID         uuid.UUID
    ActorID          uuid.UUID
    HubID            *uuid.UUID
    GrantID          *uuid.UUID
    ServiceContractID *uuid.UUID
    
    // Is this access via direct Tenant membership or via Hub delegation?
    AccessVia        string // "direct" | "hub"
}
```

Derive server-side from:
- User identity
- Hub membership (if applicable)
- Service contract status
- Grant validity
- Queue/pool scope

### 3. Hub Inbox Projection (Read Model)

New table:
- `hub_inbox_items` — unified inbox

Minimalist schema:
```sql
hub_inbox_items:
  id (PK)
  hub_id
  tenant_id
  conversation_id
  queue_id
  assigned_user_id
  customer_name
  channel
  status
  priority
  sla_due_at
  last_activity_at
  unread_count
  metadata_json
  version
  updated_at
```

Flow:
```
Tenant Domain Event
  ├─ Conversation created / updated
  ├─ Assignment changed
  └─ SLA changed
        ↓
    Outbox event
        ↓
    Event bus (NATS)
        ↓
    Hub Inbox Projector (worker)
        ↓
    hub_inbox_items (upsert/delete)
```

Read API: `GET /hubs/{hub_id}/inbox?tenant_id=...&limit=50`

### 4. Tenant Integration Gateway

Unify credential + capability management:

```sql
integration_instances:
  id (PK)
  tenant_id (FK)
  provider (e.g., "K3G", "WAHA", "Zendesk", "Salesforce")
  integration_type (e.g., "channel", "crm", "billing", "nms")
  status
  configuration_metadata_json
  created_at

integration_capabilities:
  id (PK)
  integration_instance_id (FK)
  capability (e.g., "CUSTOMER_READ", "TICKET_CREATE", "SERVICE_ORDER_CREATE")
  enabled
```

Credentials always via secret manager (HashiCorp Vault or equivalent).

**Key invariant**: No operation can select its own credentials or tenant. Server resolves from EffectiveTenantContext.

### 5. External Action Receipt & Idempotency

New table:
```sql
external_action_receipts:
  id (PK)
  tenant_id
  integration_instance_id
  action_type (e.g., "CREATE_TICKET", "CREATE_SERVICE_ORDER")
  external_id (e.g., provider's ticket ID)
  status (pending, success, failure, reconciling)
  
  actor_user_id
  hub_id
  conversation_id
  correlation_id
  idempotency_key
  
  sanitized_request_metadata
  sanitized_response_metadata
  
  created_at
  confirmed_at
  updated_at
```

Dedupe via idempotency_key before write.

### 6. Weighted Routing Engine

Inputs:
- Authorization check (hard constraint)
- Queue scope (hard constraint)
- Online/offline (hard constraint)
- Capacity (0–max_capacity)
- Weighted load by skill
- SLA urgency
- Customer affinity (same tenant only)
- Channel fit
- Skill match

Output: Scored list of eligible agents.

### 7. Agent Workspace UI

Surfaces:
1. **Hub Inbox** — unified work queue (my tenants only)
2. **Tenant Context Bar** — current tenant identity (logo, short code, accent)
3. **Conversation Cockpit** — customer + history + playbook
4. **Customer Brief** — 5-minute context summary
5. **Agent Copilot** — next-best-action + diagnostics + tool orchestration
6. **Safe Composer** — confirm tenant before sending
7. **Supervisor Command Center** — hub-wide dashboard

### 8. Feature Flags

Required:
```
OMNIRA_HUB_WORKSPACE_ENABLED
OMNIRA_HUB_ROUTING_ENABLED
OMNIRA_HUB_INBOX_ENABLED
OMNIRA_INTEGRATION_GATEWAY_ENABLED
OMNIRA_WEIGHTED_CAPACITY_ENABLED
OMNIRA_AGENT_COPILOT_ENABLED
```

Tenant-scoped where applicable.

## Acceptance Criteria (Sample)

| ID | Criterion |
|----|-----------|
| MT-AT-001 | Session in Tenant A cannot list resources of Tenant B. |
| MT-AT-002 | Known resource ID of B rejected by session A even if forged in request. |
| MT-AT-003 | Hub agent with contract to A can open conversations in A's inbox. |
| MT-AT-004 | Same agent without contract to B cannot open B's conversations. |
| MT-AT-005 | Hub membership alone does not grant Tenant access. |
| MT-AT-006 | Contract revocation immediately blocks access to that tenant's Hub Inbox. |
| MT-AT-007 | Hub Inbox never executes one query per tenant (bounded query). |
| MT-AT-008 | Agent never receives system_admin role for Hub access. |
| MT-AT-009 | UI identifies Tenant by logo + shortcode + accent, not color alone. |
| MT-AT-010 | Composer always shows effective Tenant before sending. |
| MT-AT-011 | Draft A never appears in conversation B. |
| MT-AT-012 | External action receipt includes Correlation ID, Tenant, Hub, Actor. |
| MT-AT-013 | Double click / network retry does not duplicate Service Order. |
| MT-AT-014 | AI tool cannot choose Tenant or Credentials arbitrarily. |
| MT-AT-015 | Webhook resolves Tenant via Integration Instance, not payload. |
| MT-AT-016 | Revoked contract invalidates stale projection items. |

## Technical Constraints

1. **Database**: PostgreSQL, RLS enforced, no bypass via system_admin for agents
2. **Authorization**: RBAC + ABAC + Effective Grants (server-derived)
3. **Idempotency**: All critical writes require idempotency key + deduplication
4. **Audit**: Every action logged with correlation_id, tenant_id, hub_id, actor
5. **Performance**: Hub Inbox complexity O(1) per Tenant boundary, not O(n queries)
6. **Realtime**: Tenant filtering applied before WebSocket/SSE delivery

## Migration Strategy

**Expand/Contract**:

1. Add Hub schema
2. Backfill safe defaults
3. Dual-read/dual-write only if necessary
4. Validate + test
5. Migrate consumers
6. Activate feature flag
7. Observe
8. Deprecate legacy
9. Remove old schema

**Preserve**: All existing single-tenant functionality during transition.

## Delivery Phases

```
Phase 1: PRD + ADRs + Acceptance (this document)
Phase 2: Hub domain model + RLS hardening
Phase 3: EffectiveTenantContext v2 + tests
Phase 4: Hub RLS + authorization + Codex review
Phase 5: Hub Inbox projection + reconciliation
Phase 6: Routing, presence, capacity
Phase 7: Tenant Integration Gateway
Phase 8: Migrate existing integrations
Phase 9: UX/Frontend (Hub Workspace)
Phase 10: AI / Tools / Knowledge isolation
Phase 11: Realtime / Cache / Storage hardening
Phase 12: Observability & operations
Phase 13: Security / Adversarial gate (Codex)
Phase 14: E2E / Performance / Migration proof
Phase 15: Cleanup & documentation
```

## Rollout

1. Develop on `feat/multitenant-service-hub` branch
2. Feature flags all Hub access
3. Deploy with flags off (code-only)
4. Gradual tenant enablement
5. Pilot (K3G Hub) before general availability

---

**Owner**: Engineering  
**Baseline commit**: 6d36f67  
**Last updated**: 2026-10-07
