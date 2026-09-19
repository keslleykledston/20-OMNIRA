# Routing / Supervisor Port

## Fonte Deskcomm

Estudar:

```text
docs/specs/04-spec-pipeline-attendance.md
docs/specs/13-spec-governanca-atendimento.md
```

Pesquisar:
- claim;
- transfer;
- assignment;
- attendant_availability;
- routing;
- supervisor;
- heartbeat;
- queue.

## Modelo OMNIRA

```text
Department
Queue
AgentAvailability
Assignment
AssignmentEvent
RoutingPolicy
```

## MVP

```text
manual
round_robin
department-based
```

Não portar roteamento avançado antes do uso.

## Claim

Claim deve ser atômico.

Duas requisições concorrentes para a mesma conversa:
- apenas uma vence;
- a outra recebe conflito sem sobrescrever.

## AssignmentEvent

Append-only:

```text
conversation_id
from
to
changed_by
reason
created_at
tenant_id
```

## Supervisor

Primeira versão:
- waiting;
- active;
- TME;
- TMA;
- SLA risk;
- agents online/busy;
- queue breakdown.

Não construir BI.
