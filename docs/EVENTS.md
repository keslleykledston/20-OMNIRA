# OMNIRA Event API Documentation

## Overview

OMNIRA uses event-driven architecture with NATS JetStream for asynchronous processing and event sourcing.

**Event API Version:** 0.1.0 (R0.1 Tenant Foundation)

## Quick Start

### Message Broker
- Development: `nats://localhost:4222`
- Production: `nats://nats.omnira.local:4222` (TBD)

### Event Flow

```
Command (e.g., CreateTenant)
    ↓
Domain Aggregate (Tenant)
    ↓
Event recorded to Outbox (same transaction)
    ↓
Worker poll → NATS JetStream publish
    ↓
Event Subscribers listen on subjects
```

## AsyncAPI Specification

The complete AsyncAPI 3.0.0 specification is available at:

**File:** `docs/async/asyncapi.yaml`

To view interactively:
1. Copy `asyncapi.yaml` content
2. Paste into [AsyncAPI Editor](https://editor.asyncapi.com)
3. Explore channels, operations, and payloads

## Events

### Tenant Events

#### `events.tenant.created`
**Subject:** `events.tenant.created`

Published when a new tenant is created in the system.

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "tenant.created",
  "aggregate_type": "tenant",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440000",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440100",
  "timestamp": "2024-09-18T18:30:00Z",
  "payload": {
    "legal_name": "Acme Corporation",
    "isolation_profile": "standard",
    "status": "active"
  }
}
```

#### `events.tenant.deactivated`
**Subject:** `events.tenant.deactivated`

Published when a tenant is deactivated. Signals end of service.

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440001",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "tenant.deactivated",
  "aggregate_type": "tenant",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440000",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440101",
  "timestamp": "2024-09-18T18:31:00Z",
  "payload": {
    "status": "inactive",
    "reason": "user_requested"
  }
}
```

#### `events.tenant.suspended`
**Subject:** `events.tenant.suspended`

Published when a tenant is suspended (e.g., billing issue).

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440002",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "tenant.suspended",
  "aggregate_type": "tenant",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440000",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440102",
  "timestamp": "2024-09-18T18:32:00Z",
  "payload": {
    "status": "suspended",
    "reason": "billing_overdue"
  }
}
```

### Membership Events

#### `events.membership.created`
**Subject:** `events.membership.created`

Published when a user is granted membership in a tenant.

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440003",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "membership.created",
  "aggregate_type": "membership",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440201",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440103",
  "timestamp": "2024-09-18T18:33:00Z",
  "payload": {
    "user_id": "550e8400-e29b-41d4-a716-446655440002",
    "role_id": "admin",
    "status": "active"
  }
}
```

#### `events.membership.revoked`
**Subject:** `events.membership.revoked`

Published when a user's membership is revoked. Signals access removal.

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440004",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "membership.revoked",
  "aggregate_type": "membership",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440201",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440104",
  "timestamp": "2024-09-18T18:34:00Z",
  "payload": {
    "user_id": "550e8400-e29b-41d4-a716-446655440002",
    "status": "revoked",
    "reason": "user_requested"
  }
}
```

#### `events.membership.deactivated`
**Subject:** `events.membership.deactivated`

Published when a membership is deactivated.

**Payload:**
```json
{
  "id": "550e8400-e29b-41d4-a716-446655440005",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "membership.deactivated",
  "aggregate_type": "membership",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440201",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440105",
  "timestamp": "2024-09-18T18:35:00Z",
  "payload": {
    "user_id": "550e8400-e29b-41d4-a716-446655440002",
    "status": "inactive"
  }
}
```

## Event Structure

All events follow a consistent envelope with correlation tracking:

```json
{
  "id": "550e8400-e29b-41d4-a716-446655440000",
  "tenant_id": "550e8400-e29b-41d4-a716-446655440000",
  "event_type": "tenant.created",
  "aggregate_type": "tenant",
  "aggregate_id": "550e8400-e29b-41d4-a716-446655440000",
  "correlation_id": "f47ac10b-58cc-4372-a567-0e02b2c3d479",
  "causation_id": "550e8400-e29b-41d4-a716-446655440100",
  "timestamp": "2024-09-18T18:30:00Z",
  "payload": {
    // domain-specific data
  }
}
```

**Fields:**
- **id** (UUID) — Unique event identifier
- **tenant_id** (UUID) — Tenant this event belongs to
- **event_type** (string) — Event type (e.g., `tenant.created`)
- **aggregate_type** (string) — Domain aggregate (`tenant`, `membership`, `user`)
- **aggregate_id** (UUID) — ID of the aggregate that changed
- **correlation_id** (UUID) — Request correlation ID for distributed tracing
- **causation_id** (UUID) — Parent command/event ID (for causal chains)
- **timestamp** (ISO 8601) — When the event occurred (UTC)
- **payload** (object) — Domain-specific event data

## Delivery Guarantees

### At-Least-Once Delivery

NATS JetStream ensures events are delivered at least once:
- Events persisted before publishing
- Consumer acks track delivery progress
- Redelivery on consumer failure

**Consumer Implications:**
- Consumers MUST handle duplicate events
- Use `event.id` as idempotent key
- Track seen event IDs to deduplicate

### Event Ordering

Events are **ordered per tenant**:
- All events for tenant A processed in sequence
- Events for different tenants may be interleaved
- Within tenant: causality preserved (parent before child)

### Isolation

Row-level security (RLS) ensures cross-tenant data isolation:
- Event producer can only publish for own tenant
- Event consumer can only see events for subscribed tenants
- Database enforces tenant_id at query level

## Publishing Strategy

The worker publishes events using the **outbox pattern**:

1. **Record** event in outbox table (same transaction as domain change)
2. **Poll** outbox for unpublished events (configurable interval)
3. **Publish** to NATS JetStream with idempotent key
4. **Mark** published (update outbox.published_at)
5. **Retry** with exponential backoff on failures

**Retry Strategy:**
```
Attempt 1: immediate
Attempt 2: 1s delay
Attempt 3: 2s delay
Attempt 4: 4s delay
After attempt 4: stuck in outbox for manual investigation
```

## Consuming Events

### NATS CLI

Subscribe to all tenant events:
```bash
nats sub 'events.tenant.*'
```

Subscribe to all events for a specific aggregate type:
```bash
nats sub 'events.membership.*'
```

### Go Application

```go
import "github.com/nats-io/nats.go"
import "github.com/nats-io/nats.go/jetstream"

nc, _ := nats.Connect("nats://localhost:4222")
js, _ := jetstream.New(nc)

// Create consumer
sub, _ := js.Subscribe("events.tenant.*", func(msg jetstream.Msg) {
    // Handle event
    var event Event
    json.Unmarshal(msg.Data(), &event)
    
    // Process event
    handleTenantEvent(&event)
    
    // Ack after processing
    msg.Ack()
})

defer sub.Unsubscribe()
```

### Idempotency

Always deduplicate by event ID:
```go
type EventProcessor struct {
    seen map[uuid.UUID]bool
    mu   sync.Mutex
}

func (p *EventProcessor) Process(event *Event) error {
    p.mu.Lock()
    if p.seen[event.ID] {
        p.mu.Unlock()
        return nil // already processed
    }
    p.seen[event.ID] = true
    p.mu.Unlock()
    
    // Handle event safely
    return p.handle(event)
}
```

## Observability

### Correlation IDs

All events include `correlation_id` for distributed tracing:
```
HTTP Request → X-Correlation-ID header
    ↓
API creates command with correlation_id
    ↓
Domain event inherits correlation_id
    ↓
Event published with same correlation_id
    ↓
OpenTelemetry spans linked by correlation_id
```

### Metrics

Worker exports event publishing metrics:
```
omnira_events_published_total{event_type="tenant.created"} 42
omnira_events_failed_total{event_type="tenant.created"} 1
omnira_outbox_events_pending{tenant_id="..."} 5
```

View metrics on worker at `http://localhost:9090/metrics`

## Related Documentation

- **REST API:** See [`docs/API.md`](./API.md)
- **Architecture:** See [`docs/architecture/`](./architecture/)
- **Worker:** See [`docs/architecture/WORKER.md`](./architecture/WORKER.md)
