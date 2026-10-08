# Essential ADRs — Service Hub Epic

## ADR-0025: Hub Delegation Model

**Status**: Proposed  
**Date**: 2026-10-07

### Context

OMNIRA needs to support delegated operations where a Service Hub (BPO) operates on behalf of multiple tenants without becoming their employee or gaining system-wide permissions.

### Decision

1. **Hub membership** is orthogonal to Tenant membership.
2. **Service contract** explicitly grants Hub→Tenant access (active/revoked).
3. **Work pool** groups agents within a Hub by capability and capacity.
4. **Effective access** is derived server-side via contract + membership + grant validity.
5. Hub agents **never** receive `system_admin` or `tenant_admin` roles.
6. Hub agents receive `hub_agent` role with `hub.read` + `hub.manage` permissions.
7. **Access via Hub** is tracked separately from direct Tenant membership (audit trail).

### Implementation

```sql
CREATE TABLE service_hubs (
  id UUID PRIMARY KEY,
  name VARCHAR,
  status VARCHAR CHECK (status IN ('active', 'suspended')),
  created_at TIMESTAMP
);

CREATE TABLE hub_memberships (
  id UUID PRIMARY KEY,
  user_id UUID NOT NULL,
  hub_id UUID NOT NULL REFERENCES service_hubs(id),
  role_id UUID NOT NULL,
  created_at TIMESTAMP,
  UNIQUE (user_id, hub_id)
);

CREATE TABLE hub_tenant_service_contracts (
  id UUID PRIMARY KEY,
  hub_id UUID NOT NULL REFERENCES service_hubs(id),
  tenant_id UUID NOT NULL,
  status VARCHAR CHECK (status IN ('active', 'suspended', 'revoked')),
  valid_from TIMESTAMP,
  valid_until TIMESTAMP,
  service_scope TEXT, -- JSON: queues, capabilities, etc.
  created_at TIMESTAMP,
  UNIQUE (hub_id, tenant_id)
);

CREATE TABLE work_pools (
  id UUID PRIMARY KEY,
  hub_id UUID NOT NULL REFERENCES service_hubs(id),
  name VARCHAR,
  created_at TIMESTAMP
);

CREATE TABLE work_pool_members (
  id UUID PRIMARY KEY,
  work_pool_id UUID NOT NULL REFERENCES work_pools(id),
  user_id UUID NOT NULL,
  created_at TIMESTAMP
);
```

### Verification

- RLS policy: `hub_inbox_items` only accessible if hub+tenant contract is active
- Test: Revoke contract, verify immediate access block (including stale projection)
- Test: Agent with Hub membership but no contract cannot access tenant

---

## ADR-0026: EffectiveTenantContext v2

**Status**: Proposed  
**Date**: 2026-10-07

### Context

TenantContext currently models direct Tenant membership. Hub agents need a parallel context that:
- Resolves to a single Tenant per operation
- Proves authorization server-side
- Carries audit metadata

### Decision

Extend TenantContext with:

```go
type EffectiveTenantContext struct {
    TenantID             uuid.UUID
    ActorUserID          uuid.UUID
    
    // Access path
    AccessVia            string // "direct" | "hub"
    HubID                *uuid.UUID
    ServiceContractID    *uuid.UUID
    EffectiveGrantID     *uuid.UUID
    
    // Audit
    CorrelationID        string
    Scope                []string // "read" | "write" | "admin"
}
```

Derive from:
1. Validate session (who is user)
2. Locate resource (which tenant)
3. Check membership (direct) OR (hub + contract + grant)
4. Validate active status (not revoked/expired)
5. Set RLS session variables

### Implementation

```go
// pseudocode
func (r *repo) WithEffectiveTenantContext(
    ctx context.Context,
    userID uuid.UUID,
    targetTenantID uuid.UUID,
    hubID *uuid.UUID, // optional: if hub access
) (*EffectiveTenantContext, error) {
    
    // Direct membership
    if membership, err := r.GetTenantMembership(ctx, userID, targetTenantID); err == nil {
        return &EffectiveTenantContext{
            TenantID: targetTenantID,
            ActorUserID: userID,
            AccessVia: "direct",
        }, nil
    }
    
    // Hub delegated access
    if hubID != nil {
        contract, err := r.GetActiveServiceContract(ctx, *hubID, targetTenantID)
        if err != nil || contract.Status != "active" {
            return nil, ErrNotAuthorized
        }
        
        hubMem, err := r.GetHubMembership(ctx, userID, *hubID)
        if err != nil {
            return nil, ErrNotAuthorized
        }
        
        grant, err := r.GetEffectiveGrant(ctx, *hubID, userID, targetTenantID)
        if err != nil {
            return nil, ErrNotAuthorized
        }
        
        return &EffectiveTenantContext{
            TenantID: targetTenantID,
            ActorUserID: userID,
            AccessVia: "hub",
            HubID: hubID,
            ServiceContractID: &contract.ID,
            EffectiveGrantID: &grant.ID,
        }, nil
    }
    
    return nil, ErrNotAuthorized
}
```

### Verification

- Test: Direct member can access
- Test: Hub member without contract cannot access
- Test: Contract revoked invalidates prior access
- Test: RLS session variables set correctly
- Codex adversarial review mandatory

---

## ADR-0027: Hub Inbox Read Model

**Status**: Proposed  
**Date**: 2026-10-07

### Context

A unified inbox for Hub agents querying multiple tenants must:
- Not execute O(n) queries per tenant
- Respect authorization (only contracted tenants)
- Update via event-driven projection
- Support revocation (stale item still unauthorized)

### Decision

1. **Materialized view** (`hub_inbox_items`):
   ```sql
   hub_id | tenant_id | conversation_id | queue_id | priority | sla_due_at | ...
   ```
   Minimal fields for triage; source of truth remains Tenant conversation.

2. **Projection flow**:
   ```
   Tenant event (conversation.created, assignment.changed)
      ↓ Outbox
      ↓ NATS event
      ↓ Hub projector worker
      ↓ upsert hub_inbox_items
   ```

3. **Authorization at read-time**:
   ```go
   // Fetch inbox
   items, err := r.GetHubInbox(ctx, hubID, limit, cursor)
   // items already filtered by active contracts
   // If contract revoked between projection and read, RLS blocks it
   ```

4. **Reconciliation**:
   ```
   Daily/weekly sweep:
   - DELETE from hub_inbox_items
     WHERE hub_id NOT IN (
       SELECT hub_id FROM hub_tenant_service_contracts
       WHERE status = 'active'
     )
   ```

### Verification

- Test: Contract revoked → item invisible immediately (via RLS re-check)
- Test: Projection lag does not bypass auth
- Test: Inbox bounded query (✓ no N+1)
- Performance: cursor pagination <100ms

---

## ADR-0028: Tenant Integration Gateway

**Status**: Proposed  
**Date**: 2026-10-07

### Context

External integrations (ERP, CRM, billing) must be:
- Isolated per Tenant
- Versioned (capable upgrades without tenant impact)
- Idempotent (no double-create from retry)
- Auditable (who, what, when, why)

### Decision

1. **Integration instance** = unique connection to external provider

   ```sql
   integration_instances:
     id
     tenant_id (FK)
     provider (e.g., "K3G", "Salesforce", "Zendesk")
     integration_type (channel | crm | billing | nms)
     status (active | inactive | error)
     environment (prod | sandbox)
     configuration JSON (non-secret)
     credential_reference (vault path)
   ```

2. **Integration capabilities** = fine-grained permissions

   ```sql
   integration_capabilities:
     id
     integration_instance_id (FK)
     capability (e.g., CUSTOMER_READ, TICKET_CREATE, SERVICE_ORDER_CREATE)
     enabled BOOLEAN
   ```

3. **Execution context** = always server-derived

   ```go
   type IntegrationExecutionContext struct {
       TenantID                uuid.UUID
       IntegrationInstanceID   uuid.UUID
       Provider                string
       Capability              string
       
       ActorUserID             uuid.UUID
       HubID                   *uuid.UUID
       ConversationID          uuid.UUID
       
       CorrelationID           string
       IdempotencyKey          string
   }
   ```

4. **No client selection** of Tenant/Integration:
   ```go
   // WRONG
   tool.CreateServiceOrder(tenantID, integrationID, ...args)
   
   // RIGHT
   tool.CreateServiceOrder(...args)
   // Server resolves tenant/integration from EffectiveTenantContext
   ```

### Verification

- Test: AI tool cannot choose tenant
- Test: Different tenant cannot use A's integration
- Test: Capability denied → action rejected
- Test: Credential never sent to frontend/AI
- Codex adversarial review mandatory

---

## ADR-0029: External Write Idempotency

**Status**: Proposed  
**Date**: 2026-10-07

### Context

Writing to external systems (Ticket create, Service Order create, etc.) must be idempotent to handle:
- Browser retry
- Network timeout
- Double click
- Worker retry

### Decision

1. **Local idempotency key** (UUID):
   ```
   correlation_id (user action)
   idempotency_key (derived: hash of action + context)
   ```

2. **Durable attempt tracking**:
   ```sql
   external_action_receipts:
     id
     tenant_id
     integration_instance_id
     action_type (CREATE_TICKET, CREATE_SERVICE_ORDER)
     
     idempotency_key UNIQUE NOT NULL
     status (pending | in_flight | success | failure | reconciling)
     
     external_id (provider's ID, if known)
     
     actor_user_id
     hub_id
     conversation_id
     correlation_id
     
     sanitized_request
     sanitized_response
     
     created_at
     confirmed_at
   ```

3. **Flow**:
   ```
   1. Check dedupe: SELECT WHERE idempotency_key
   2. If found + success → return cached result
   3. If found + in_flight → wait + retry
   4. If not found → INSERT (pending)
   5. Call external API
   6. On success → UPDATE status=success, external_id
   7. On timeout → status=reconciling (reconciler checks provider)
   8. On failure → status=failure, try backoff retry
   ```

### Verification

- Test: Create Ticket twice with same idempotency_key → 1 ticket created
- Test: Timeout (outcome_unknown) → enters reconciliation, not blind retry
- Test: Receipt contains audit metadata, not secrets

---

## ADR-0030: Hub RLS Access Path

**Status**: Proposed  
**Date**: 2026-10-07

### Context

Hub agents must access multiple Tenants' resources via RLS, but:
- Never via `system_admin`
- Always via explicit grant
- Revocation must be immediate

### Decision

1. **RLS policy for Hub access**:
   ```sql
   -- conversations table (example)
   CREATE POLICY hub_tenant_access ON conversations AS SELECT
     USING (
       tenant_id = (SELECT current_setting('omnira.tenant_id')::uuid)
     )
   FOR SELECT;
   ```

2. **Session variable setup** (per request):
   ```go
   // After EffectiveTenantContext derived
   _, _ = conn.Exec(ctx, 
       `SET omnira.tenant_id = $1;
        SET omnira.actor_id = $2;
        SET omnira.via = $3;`,
       ctx.TenantID,
       ctx.ActorUserID,
       ctx.AccessVia, // "direct" | "hub"
   )
   ```

3. **Validation before session setup**:
   ```go
   // Pseudocode
   contract, _ := GetActiveServiceContract(ctx, hubID, tenantID)
   if contract == nil || contract.Status != "active" {
       return ErrNotAuthorized
   }
   ```

4. **No bypass**:
   - Hub agent never becomes tenant_admin
   - No `is_superuser` for Hub operations
   - No context.Background() for tenant-scoped queries

### Verification

- Test: RLS enforces tenant boundary even if session variable wrong
- Test: Contract revocation blocks subsequent queries
- Test: Revoked contract, stale session → RLS denies
- Codex mandatory review

---

## ADR-0031: External Webhook Isolation

**Status**: Proposed  
**Date**: 2026-10-07

### Context

Webhooks from external providers (WAHA, Zendesk, etc.) must:
- Determine Tenant from trusted source (Integration Instance)
- Never trust `tenant_id` from webhook payload
- Prevent replay attacks
- Deduplicate

### Decision

1. **Webhook ingress flow**:
   ```
   1. Parse webhook (TLS required, signature check)
   2. Find Integration Instance by signature/token (trusted)
   3. Resolve Tenant from IntegrationInstance.tenant_id
   4. Check idempotency (event_id, provider)
   5. Process in Tenant context
   ```

2. **Dedupe table**:
   ```sql
   webhook_deduplication:
     id
     integration_instance_id (FK)
     provider_event_id (e.g., "msg_123456")
     processed_at
     UNIQUE (integration_instance_id, provider_event_id)
   ```

3. **Never do**:
   ```go
   // WRONG
   tenantID := payload.TenantID // trust client
   
   // RIGHT
   instance, _ := GetIntegrationInstance(signature)
   tenantID := instance.TenantID // trust provider
   ```

### Verification

- Test: Forged webhook with different tenant_id rejected
- Test: Replay (same event_id) deduped
- Test: Webhook determines tenant, not payload

---

## ADR-0032: Agent Copilot Tool Authorization

**Status**: Proposed  
**Date**: 2026-10-07

### Context

AI tools (create_ticket, create_service_order, query_billing) must:
- Never allow LLM to choose Tenant or Credentials
- Inherit EffectiveTenantContext
- Validate Capability before execution

### Decision

1. **Tool contract** (example):
   ```go
   type CreateServiceOrderTool struct {
       Tenant   EffectiveTenantContext // from context, read-only
       Repo     IntegrationRepo        // credentials resolve via Tenant
   }
   
   func (t *CreateServiceOrderTool) CreateServiceOrder(
       reason string,
       type string,
       notes string,
   ) (result, error) {
       // No tenant_id, integration_id, credentials parameters
       
       instance, _ := t.Repo.GetPrimaryIntegration(
           t.Tenant.TenantID,
           "nms", // fixed by tool type
       )
       
       // Validate capability
       cap, _ := t.Repo.GetCapability(
           instance.ID,
           "SERVICE_ORDER_CREATE",
       )
       if !cap.Enabled {
           return nil, ErrCapabilityNotEnabled
       }
       
       // Execute in Tenant context
       ...
   }
   ```

2. **Tool cannot override**:
   - Tenant
   - Integration instance
   - Credentials
   - Capability check

3. **Prompt engineering**: AI instructions must forbid:
   - `tenant_id = ...` parameters
   - `use different provider`
   - `fetch credential`

### Verification

- Test: Prompt injection "use Tenant B" → tool still uses Tenant A
- Test: Capability disabled → action rejected
- Codex adversarial review: prompt injection resistance

---

## ADR-0033: Frontend Stable Shell + Tenant Identity Layer

**Status**: Proposed  
**Date**: 2026-10-07

### Context

UI must support multi-tenant operations without fatigue or identity errors:
- Agent should not need to manually select Tenant
- Tenant identity must be instantly recognizable
- Context change (Tenant switch) must preserve shell structure

### Decision

1. **Stable Shell**:
   ```
   ┌─────────────────────────────────────────┐
   │ OMNIRA    [Hub Inbox] [Copilot] [Menu] │ ← stable
   ├─────────────────────────────────────────┤
   │ [ISR] ISP Roraima | WhatsApp | SLA 08:42 │ ← tenant identity
   ├─────────────────────────────────────────┤
   │ [Conversation Cockpit - content varies] │ ← tenant-specific
   └─────────────────────────────────────────┘
   ```

2. **Tenant identity indicators** (visual):
   ```
   Logo + Short Code + Accent + Name
   
   NOT color alone (accessibility)
   ```

3. **Context switch safety**:
   ```
   - Open conversation A (Tenant A)
   - Conversation data loads Tenant A
   - Draft A stored scoped to (Tenant A, Conversation A)
   - Switch to conversation B (Tenant B)
   - Draft B loads (not A)
   - Composer shows Tenant B identity
   ```

4. **Sensitive action confirmation**:
   ```
   When executing external action:
   ┌──────────────────────────────┐
   │ Executar em: [ISR] ISP Roraima │
   │ Cliente: José Carlos         │
   │ Contrato: 21873              │
   │ Destino: K3G (IXC)           │
   │ Ação: Abrir Ordem de Serviço │
   │ [Confirmar] [Cancelar]       │
   └──────────────────────────────┘
   ```

### Verification

- Test: Agent works in Hub Inbox without tenant selector
- Test: Tenant identity recognized instantly (logo + code)
- Test: Draft A never appears in Conversation B
- E2E: Switch tenant 5x, all context preserved correctly

---

## ADR-0034: Weighted Capacity Routing

**Status**: Proposed  
**Date**: 2026-10-07

### Context

Round-robin assignment ignores reality:
- WhatsApp task ≠ phone call
- Incident ≠ billing inquiry
- Agents have varying capacity

### Decision

1. **Capacity model per agent**:
   ```sql
   agent_capacity:
     user_id
     hub_id
     max_capacity INTEGER (units)
     current_capacity INTEGER (units)
     last_updated
   ```

2. **Weight per task type** (configurable per Tenant + Queue):
   ```
   WhatsApp simple: 1 unit
   Billing complex: 2 units
   Support Level 1: 2 units
   Critical incident: 4 units
   Phone call: 3 units
   ```

3. **Routing logic**:
   ```
   Hard constraints:
     - Authorized (contract + capability)
     - Online
     - Enough free capacity
   
   Scoring:
     - SLA urgency
     - Skill match
     - Weighted capacity usage
     - Customer continuity
   ```

### Verification

- Test: Agent at max_capacity not assigned new task
- Test: Critical incident can override lower-priority queue
- Test: Weight configurable per tenant (no global hardcoded)

---

## ADR-0035: Customer Affinity without Cross-Tenant Leak

**Status**: Proposed  
**Date**: 2026-10-07

### Context

Continuity is valuable (same agent = faster resolution), but must never:
- Cross tenant boundaries
- Ignore authorization
- Prioritize affinity over safety

### Decision

1. **Affinity scope: Tenant + Customer only**:
   ```sql
   customer_agent_affinity:
     tenant_id
     customer_id
     user_id
     last_interaction
     interaction_count
   ```

2. **Routing scoring**:
   ```
   IF authorized(agent, tenant) AND online AND capacity available:
       IF hasAffinity(agent, tenant, customer):
           score += 10
   ```

3. **Cannot cross tenant**:
   ```
   // WRONG
   agent_id = FindBestAgentAcrossTenants(customer_phone, ...)
   
   // RIGHT
   agent_id = FindBestAgent(
       tenant_id,  // Tenant of current conversation
       queue_id,
       customer_id,
       skills,
   )
   ```

### Verification

- Test: Affinity never crosses Tenant boundary
- Test: Affinity ignored if agent not authorized
- Test: Same phone number in A + B → separate agents possible

---

## Consolidated Timeline

1. **Phase 2** (this sprint): ✓ ADRs + approval
2. **Phase 3**: Implement Hub schema + RLS hardening
3. **Phase 4**: EffectiveTenantContext v2 + authorization layer
4. **Phase 5+**: Projections, gateway, UI, security gates

---

**Baseline**: 6d36f67  
**Status**: Proposed for team review  
**Next**: Codex adversarial review (Phase 2 gate)
