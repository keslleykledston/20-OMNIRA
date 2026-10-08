# OMNIRA — Phase 0 Audit

**Baseline**: commit `6d36f67` (main)  
**Status**: In progress (2026-10-07)

## Git State

- Current branch: `main`
- Working tree: **clean**
- Recent PRs: feat/outbound-attachments, feat/mobile-core-auth
- Schema: 92 migrations (000001..000092)

## Stack Confirmed

✓ Backend: Go  
✓ Frontend: Next.js + TypeScript  
✓ DB: PostgreSQL  
✓ Messaging: NATS + JetStream  
✓ Cache: Valkey (presence)  
✓ Telemetry: OpenTelemetry  
✓ Strategy: Monolithic modular + workers  

## Schema State

- 92 migrations active
- Latest:
  - 000092_outbound_media (2026-10-07)
  - 000091_mobile_device_sessions (2026-10-07)
  - 000090_contact_kind_internal (2026-10-07)

## Tenancy & RLS State

**Current Implementation**:
- Tenancy module: `internal/tenancy/{domain,ports,application,adapters}`
- Pattern: **PostgreSQL RLS-based isolation**
- Structure: `tenant_id` obrigatório per resource
- Session: `TenantContext` derived from user membership
- Enforcement: `WITH (check_expression=(tenant_id = current_setting(...)))`

**Audited Risk**:
- ✗ No Service Hub layer yet
- ✗ No Hub membership model
- ✗ No delegated access (Hub→Tenant)
- ✗ No EffectiveTenantContext for cross-tenant agents
- ✗ No Hub Inbox projection
- ✗ No Integration Gateway pattern

## Existing Modules (Relevant)

### High Priority
- `internal/tenancy/` — Tenant membership, context
- `internal/rbac/` — Role-based access control
- `internal/platform/` — Session, DB, auth
- `internal/channels/` — Channel ingest (WAHA, Meta, etc.)
- `internal/messages/` — Message lifecycle
- `internal/inbox/` — Conversation inbox
- `internal/contacts/` — Contact management
- `internal/tickets/` — Ticketing integration
- `internal/flows/` — Automation/flows
- `internal/attendance/` — New (000086+): call/meeting context
- `internal/identity/` — User identity
- `internal/iam3/` — Identity & auth v3
- `internal/ai/` — AI summarization/tools
- `internal/outbox/` — Durable event outbox
- `internal/tool/` — Tool runtime
- `internal/worker/` — Worker/job execution
- `internal/media/` — Media handling (ADR-0024)

### Integration Adapters
- `internal/channels/adapters/` — WAHA, Meta, custom webhooks
- `internal/tickets/adapters/` — ERP/CRM ticketing
- (No unified Integration Gateway yet)

## RLS Completeness

**Test file**: `internal/platform/db/rls_completeness_test.go`  
→ Validates RLS enforcement on all tenant-scoped tables  
→ Tests cross-tenant isolation  
✓ Test exists and runs

## Authorization Model (Current)

```text
User → Tenant Membership → Role → Permissions
                    ↓
                TenantContext (server-derived)
                    ↓
                Repository/Service (RLS enforced)
```

**Missing**:
```text
Hub Membership
  ↓
Service Contract (Tenant scope)
  ↓
Work Pool / Grants
  ↓
EffectiveTenantContext
```

## Frontend State

- **Path**: `apps/web/`
- **Stack**: Next.js 14, React, TypeScript
- **No Hub Workspace yet** (single-tenant only)
- **No unified Inbox** (Tenant-specific)
- **No agent workspace** (supervisor focus)

## Delivery & ADRs

**Completed ADRs** (sample):
- ADR-0022: Mobile device sessions (MOBILE.1)
- ADR-0024: Outbound attachments
- ADR-0017: Conversation intelligence (not deployed)
- ADR-0016: Media pipeline (M1 deployed)

**Missing ADRs for this epic**:
- Service Hub delegation
- EffectiveTenantContext v2
- Hub RLS access
- Hub Inbox projection
- Integration Gateway
- External resource qualification
- External write idempotency

## Integration Inventory

**Channel adapters** (existing):
- WAHA (WhatsApp via WAHA sandbox)
- Meta direct
- Webhook custom

**Business integrations** (partial):
- Ticketing connectors (ERP/CRM)
- No unified Gateway yet
- Credentials stored per-tenant (good)

## Tests

- Unit tests: Yes (throughout)
- Integration tests: Yes (PostgreSQL real)
- E2E vertical test: `internal/e2e/vertical_test.go`
- RLS tests: `internal/platform/db/rls_completeness_test.go`

**Coverage**: Reasonable. Need to add Hub/Integration tests.

## Observability

- OpenTelemetry: Yes
- Traces: Structured
- Metrics: Tenant scoped (sample)
- Logs: Audit baseline

## Known Risks (Migration Path)

1. **Existing code using `system_admin`**: Will need validation during Hub layer
2. **Cache/Valkey**: Presence-only (safe). Cache keys need validation.
3. **WebSocket/SSE**: Need tenant filtering before Hub Inbox
4. **Background workers**: Need EffectiveTenantContext
5. **Ticketing reconciliation**: Needs Integration Gateway adapter

## Schema Gaps (Audit)

**Missing for Hub**:
- `service_hubs` table
- `hub_memberships` table
- `hub_tenant_service_contracts` table
- `work_pools` table
- `work_pool_members` table
- `effective_access_grants` table
- `integration_instances` table (if not unified yet)
- `integration_capabilities` table

## Next Steps

1. ✅ **This audit** — complete baseline
2. **Materialize PRD + ADRs** — Phase 1
3. **Audit legacy code** — paths that bypass RLS or use system_admin
4. **Design Hub model** — database schema & authorization
5. **Implement Hub foundation** — Phase 3+

## Phase 0 Deliverables

- [ ] Repository state map
- [ ] Schema audit (RLS completeness)
- [ ] Authorization audit (legacy risks)
- [ ] Integration audit (credential handling)
- [ ] Frontend audit (multi-tenant readiness)
- [ ] Test suite audit
- [ ] ADR gaps documentation
- [ ] Mission backlog + Phase 1 PRD

---

**Auditor**: Claude Code  
**Date**: 2026-10-07  
**Baseline commit**: 6d36f67  
