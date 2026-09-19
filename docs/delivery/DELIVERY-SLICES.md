# Entregas Curtas e Funcionais

O MVP não será construído como um bloco único.

## Release 0.1 — Tenant Foundation

**Objetivo:** primeiro Tenant utilizável e seguro.

Entregar:
- auth/OIDC adapter;
- Tenant;
- User/Membership;
- RBAC;
- TenantContext;
- PostgreSQL + migrations;
- RLS/controle equivalente;
- audit básico;
- health endpoints;
- OpenTelemetry básico;
- outbox;
- NATS local/dev;
- CI;
- suite de isolamento A/B.

Demo:
1. criar Tenant A e B;
2. usuário A acessa A;
3. tentativa de acessar B é bloqueada;
4. auditoria/correlation aparece.

**Sem Hub.**

## Release 0.2 — Atendimento Single-Tenant

**Objetivo:** atendimento real sem BPO.

Entregar:
- Contact;
- Conversation;
- Message;
- Ticket;
- Queue;
- Webchat;
- inbox;
- realtime;
- atribuição manual/round-robin simples;
- anexos mínimos.

Demo:
cliente inicia webchat -> ticket entra -> operador responde -> encerra.

## Release 0.2.1 — WhatsApp

Entregar adapter oficial:
- conexão;
- webhook;
- inbound;
- outbound;
- delivery status;
- idempotência;
- media básico.

Demo:
mensagem real WhatsApp -> inbox -> resposta.

## Release 0.3 — Tools + IXC

**Objetivo:** provar resolução.

Entregar:
- `ToolExecution`;
- fila de tools;
- workers;
- retry/DLQ;
- secrets;
- IXC subscriber lookup;
- faturas;
- segunda via;
- abrir/consultar chamado.

Demo:
operador resolve solicitação IXC dentro do ticket.

## Release 0.4 — Hub BPO

**Objetivo:** adicionar multicontas sem alterar ownership dos dados.

Entregar:
- Hub;
- HubMembership;
- HubTenantGrant;
- autorização derivada;
- inbox consolidada;
- context switch;
- branding inequívoco;
- revogação;
- testes adversariais.

Demo:
mesmo operador atende A e B sem relogin e sem vazamento.

## Release 0.5 — Supervisor

- TME;
- TMA;
- SLA;
- operadores/presence;
- fila em tempo real;
- dashboards operacionais.

## Release 0.6 — Automação mínima

- flow versionado;
- message node;
- condition;
- tool node;
- handoff;
- observabilidade.

## Gate de cada release

- funcional ponta a ponta;
- teste automatizado;
- telemetria mínima;
- runbook quando existe operação nova;
- changelog;
- nenhuma dívida cross-tenant conhecida.
