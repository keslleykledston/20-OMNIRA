# Catálogo de Serviços Operacionais

## Runtime inicial

### omnira-api
HTTP API e comandos síncronos.

### omnira-worker
Executores de jobs, outbox e ferramentas; pode iniciar como um binário com pools internos.

### omnira-realtime
Pode permanecer dentro da API no início. Extrair apenas se conexões/fanout justificarem.

### postgres
Fonte de verdade.

### nats
Eventos/jobs.

### valkey
Cache/presence.

### object-storage
Anexos e artefatos.

### otel-collector
Pipeline de telemetria.

## Para cada serviço manter

```text
owner
repo/module
criticality
dependencies
SLO
dashboards
alerts
runbook
backup
restore
deployment
rollback
capacity signals
```

## Criticality

- Tier 0: tenancy/auth/postgres.
- Tier 1: API, inbound/outbound messaging, NATS.
- Tier 2: realtime, tool adapters.
- Tier 3: analytics/reporting não operacional.
