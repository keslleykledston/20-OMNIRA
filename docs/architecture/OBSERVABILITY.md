# Observabilidade e Telemetria

## Padrão

Instrumentação via **OpenTelemetry** para evitar lock-in.

Pipeline:

```text
App/Workers
  -> OTel SDK
  -> OTel Collector / Grafana Alloy
     -> Prometheus (metrics)
     -> Loki (logs)
     -> Tempo (traces)
  -> Grafana
  -> Alertmanager
```

## Correlation

Propagar:

```text
trace_id
span_id
correlation_id
tenant_id [logs/traces]
actor_id quando permitido
tool_execution_id
message_id/event_id
external_request_id
```

### Cardinalidade

**Não usar `tenant_id`, contact_id, ticket_id ou user_id como labels Prometheus de alta cardinalidade.**

Esses identificadores ficam em logs/traces. Métricas usam dimensões limitadas como:
- service;
- module;
- adapter;
- operation;
- status class;
- queue;
- environment.

## API — RED

Por endpoint/grupo:
- Rate;
- Errors;
- Duration p50/p95/p99.

Também:
- active requests;
- request size;
- response size;
- auth failures;
- 429s.

## Infra — USE

- Utilization;
- Saturation;
- Errors.

## PostgreSQL

- connections/pool saturation;
- query duration;
- locks;
- deadlocks;
- disk;
- WAL;
- replication lag;
- slow queries;
- RLS/authorization error rate.

## NATS/JetStream

- stream health/replicas;
- publish error/latency;
- consumer pending;
- consumer redelivery;
- oldest pending age;
- ack latency;
- DLQ count;
- worker throughput.

## Tool/Integration

Por adapter/operação:
- request rate;
- success/error;
- timeout;
- latency;
- retry;
- circuit breaker state;
- rate-limit hits;
- dead letters.

## Realtime

- connections ativas;
- connect/disconnect;
- reconnect rate;
- fanout latency;
- dropped/backpressured clients.

## Produto operacional

Separado de telemetria de infraestrutura:
- conversas novas;
- tickets em fila;
- TME;
- TMA;
- SLA breaches;
- operadores online.

## Alert philosophy

Page apenas quando existe ação necessária ou impacto provável no usuário.

### P1/P0 exemplos
- API indisponível;
- Postgres indisponível;
- risco de perda de dados;
- falha cross-tenant detectada;
- backlog de entrada crescendo continuamente;
- WhatsApp inbound parado para grande parcela dos tenants.

### Ticket/non-page
- aumento moderado de retry;
- erro de um Tenant por credencial IXC inválida;
- consumer lag transitório.

## Dashboards mínimos

1. Executive Health.
2. API.
3. PostgreSQL.
4. NATS/Workers.
5. Channels.
6. ERP adapters.
7. Realtime.
8. Tenant Security/Audit.
