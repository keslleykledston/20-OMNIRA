# Mensageria, Jobs e Fila de Tools

## 1. Objetivo

Separar três conceitos que costumam virar uma única "fila":

1. **Domain Events** — fatos que já aconteceram.
2. **Jobs** — trabalho assíncrono a executar.
3. **Tool Executions** — ação externa rastreável, tenant-aware e auditável.

NATS JetStream é o transporte inicial; PostgreSQL mantém o estado canônico de operações de negócio.

## 2. Sem promessa de exactly-once

Assumimos entrega **at-least-once** e construímos:
- idempotência;
- deduplicação;
- retry;
- timeout;
- DLQ;
- observabilidade.

Todo consumidor deve suportar redelivery.

## 3. Outbox Pattern

Mudança de domínio e criação do evento ocorrem na mesma transação PostgreSQL:

```text
BEGIN
  update ticket
  insert outbox_event
COMMIT

Outbox Dispatcher
  -> publish JetStream
  -> mark published
```

Isso evita o clássico dual-write `DB salvou / broker falhou`.

## 4. Inbox / Processed Message

Consumidores críticos mantêm `message_id`/`event_id` processado para que redelivery não repita efeito de negócio.

## 5. Convenção de subjects

```text
evt.<domain>.<event>.v1
job.<capability>.<task>.v1
tool.<adapter>.<operation>.v1
sys.<component>.<signal>.v1
```

Exemplos:

```text
evt.ticket.created.v1
evt.message.received.v1
job.routing.assign.v1
tool.ixc.invoice_copy.v1
tool.whatsapp.send_message.v1
sys.tool.execution_dead_lettered.v1
```

O `tenant_id` pertence ao envelope, não ao subject, para evitar explosão de subjects e acoplamento físico por Tenant.

## 6. Envelope canônico

```json
{
  "id": "uuid",
  "type": "tool.ixc.invoice_copy.v1",
  "occurred_at": "RFC3339",
  "tenant_id": "uuid",
  "actor_id": "uuid|null",
  "correlation_id": "uuid",
  "causation_id": "uuid|null",
  "idempotency_key": "string|null",
  "payload": {}
}
```

Nunca transportar:
- senha/token do ERP;
- token WhatsApp;
- connection string;
- PII desnecessária.

Workers recuperam segredo server-side usando `TenantContext`.

## 7. ToolExecution

Tabela canônica:

```text
tool_executions
- id
- tenant_id
- tool_name
- operation
- status
- priority
- input_ref / sanitized_input
- result_ref / sanitized_result
- attempt
- max_attempts
- timeout_at
- idempotency_key
- correlation_id
- created_at
- started_at
- finished_at
- last_error_code
```

Estados:

```text
PENDING -> QUEUED -> RUNNING -> SUCCEEDED
                    |   |
                    |   -> FAILED -> RETRY -> QUEUED
                    |
                    -> DEAD_LETTER

PENDING/QUEUED -> CANCELLED quando a operação suportar cancelamento
```

## 8. Pools de workers

Separar concorrência por capacidade:

```text
worker-channel-whatsapp
worker-integration-ixc
worker-routing
worker-automation
worker-notification
```

Não criar um único worker que executa tudo.

Cada pool possui:
- concurrency global;
- concurrency por Tenant;
- rate limit do provedor;
- timeout;
- retry policy;
- circuit breaker;
- métricas.

## 9. Retry

Classificar erros:

### transient
- timeout;
- 429;
- 5xx externo;
- conexão resetada.

Retry exponencial + jitter, respeitando `Retry-After`.

### permanent
- autenticação inválida;
- payload inválido;
- recurso inexistente quando definitivo;
- operação proibida.

Não retry automático.

### unknown
Poucas tentativas e depois DLQ, preservando diagnóstico.

## 10. DLQ

DLQ não é "cemitério invisível".

Ao exceder tentativas:
- atualizar `ToolExecution = DEAD_LETTER`;
- publicar evento operacional;
- alertar conforme severidade;
- permitir replay manual auditado;
- preservar `correlation_id`.

## 11. Scheduler

No MVP, agendamentos curtos e retries podem ser controlados por banco + worker scheduler.

Não introduzir Temporal no primeiro release.

Criar interface `WorkflowEngine` desde o início. Considerar Temporal quando surgirem:
- workflows de horas/dias;
- waits humanos;
- timers duráveis;
- compensações/Sagas complexas;
- dezenas de etapas;
- necessidade de retomar exatamente do ponto lógico após crash.

## 12. Backpressure

Quando o provedor degrada:
- limitar consumo;
- manter backlog durável;
- medir idade da mensagem mais antiga;
- impedir retry storm;
- priorizar mensagens/tarefas críticas;
- nunca derrubar API por saturação de integração externa.

## 13. Contratos

- HTTP: OpenAPI.
- Eventos/mensagens: AsyncAPI.
- payload versionado;
- mudanças aditivas permanecem na versão;
- breaking change cria `v2`.
