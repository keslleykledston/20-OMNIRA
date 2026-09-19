# Architecture Brief — OMNIRA

## Escolha central

OMNIRA começa como **monólito modular em Go**, com workers separados logicamente.

```text
Next.js
   |
 Nginx
   |
 Go API
   |
   +-- PostgreSQL   source of truth
   +-- NATS JS      events/jobs
   +-- Object Store attachments
   +-- Valkey       ephemeral (quando necessário)
```

## Domain modules

```text
identity
tenancy
audit
contacts
conversations
tickets
routing
channels
integrations
tools
realtime
hub
sla
automation
analytics
```

O Hub é adicionado somente depois do core Tenant.

## Security boundary

```text
Principal
  -> Authorization
  -> TenantContext
  -> Application Service
  -> Repository
```

RLS no tier compartilhado adiciona defesa em profundidade.

## Assíncrono

```text
DB transaction
 -> outbox
 -> NATS JetStream
 -> worker
 -> adapter/tool
```

Sem exactly-once. Usar at-least-once + idempotência.

## Observability

OpenTelemetry desde o R0.1.

```text
OTel -> Collector -> Metrics / Logs / Traces
```

## Evolução

Não extrair módulo para serviço até existir necessidade concreta de:
- escala;
- blast radius;
- ownership;
- runtime;
- deploy independente.

## Prioridade

Corretude > isolamento > simplicidade > performance otimizada prematuramente.
