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

## Estado de execução — WhatsApp não oficial até Inbox

- DONE: U1 runtime Docker WAHA.
- DONE: U2 lifecycle de sessão e QR no adapter.
- DONE: U3 webhook HMAC e normalização canônica.
- DONE: U4 outbound text e boundary de worker.
- DONE: U5 mídia/status no adapter.
- DONE: M02 Contact foundation.
- DONE: M03 Conversation/Message/Ticket foundation.
- DONE: M03.1/M03.2 persistência inbound atômica e idempotente ligada ao webhook WAHA.
- DONE: M03.3 persistência de mídia inbound e aplicação de `message.ack` aos status canônicos.
- DONE: M04 filas e seleção round-robin de domínio.
- DONE: M04.1 claim manual atômico, repository/application e teste de corrida.
- DONE: M04.2a atribuição round-robin atômica, capacidade/disponibilidade e actor de sistema.
- DONE: M04.2b1 Outbox transacional e subject canônico `job.routing.assign.v1`.
- DONE: M04.2b2 consumidor JetStream, resolução confiável por conversa e redelivery idempotente.
- TODO: M04.3 seleção/atribuição da fila inicial para disparar routing automaticamente no inbound.
- TODO: M05.1 API REST paginada da Inbox sob TenantContext.
- TODO: M05.2 eventos realtime SSE/WebSocket com reautorização e isolamento A/B.
- TODO: M05.3 frontend Next.js com Omnira iOS Design System; não reutilizar o Vite mock como runtime final.
- TODO: M06 chatbot/automation depois do vertical Inbox funcional.
