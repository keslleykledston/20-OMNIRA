# ADR 0006 — NATS JetStream como barramento; PostgreSQL como fonte de verdade

**Status:** Proposed  
**Data:** 2026-09-18

## Contexto

OMNIRA precisa de webhooks, eventos, jobs, tool execution, retries e backpressure sem introduzir Kafka/Temporal/microserviços precocemente.

## Decisão

Usar NATS + JetStream para transporte durável de eventos e jobs.

PostgreSQL mantém:
- estado de domínio;
- outbox;
- ToolExecution;
- idempotency/process state crítico.

Adotar semântica at-least-once + idempotência.

Valkey não será source of truth.

## Consequências

- operação inicial simples;
- caminho natural para workers concorrentes em Go;
- replay/redelivery disponível;
- recovery pode reconciliar a partir do Postgres;
- exige outbox/inbox e disciplina de idempotência.

## Evolução

Temporal pode ser introduzido atrás de `WorkflowEngine` quando houver workflows longos/duráveis complexos.
