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

### Estado atual da retomada

- Branch: `master`. HEAD: M05.4 (`1bab8b0`), sobre H0 (`022e74f`). Sem push/tag.
- Prioridade vigente: **WhatsApp não oficial (WAHA)** nas primeiras entregas. Meta Cloud fica preservado como provider oficial (D3.1–D3.3 prontos), **sem prioridade operacional**; D3.4+ congelado.
- Gate verde (Postgres real + `omnira_app`): `go build/vet/test ./...`, RLS completeness, isolation A/B, `validate-schema`, migrations up-all → down-all → up-all, `docker compose config`, imagens `Dockerfile.api`/`Dockerfile.worker` (Go 1.25), web vitest 24/24 + `tsc`.
- Como rodar integração: DB descartável no `omnira-postgres` (porta 55434) com `OMNIRA_DATABASE_URL` (owner) e `OMNIRA_APP_DATABASE_URL` = mesma URL + `options=-c role=omnira_app` (a role não tem LOGIN). As tools em `tools/` apontam `omnira_dev`; redirecione para um DB de teste.
- Migrations: última = `000024_channel_manage_permission`. `000020.down` corrigido; `000022` remove policy GUC quebrada.
- Restrições conhecidas: `DELIVERY-SLICES`/contratos OpenAPI (`contracts/openapi` vazio) ainda não documentam as rotas de inbox/assign; erros HTTP são `http.Error` texto (sem Problem Details) em todo o projeto.

### Próximos passos ordenados (caminho WAHA)

1. ~~**W1 — API de conexão/sessão WAHA**~~ ✅ DONE (ver abaixo). Falta a **UI** de conexões (criar, aceite de risco, QR com polling em `GET .../{id}` até `session_status=needs_qr`, status).
2. **W2 — M05.5 envio outbound de texto**: `POST .../messages` com idempotency key → `messages` queued + Outbox job → **ligar `internal/worker/delivery` no `omnira-worker`** (existe como biblioteca, não está no `main`) → `WahaProvider.SendText` → status/ack canônico. Retry ErrTransient/ErrPermanent.
3. **W3 — Smoke real com WAHA** (`devlikeapro/waha:gows`): parear sessão, inbound → Inbox via webhook, resposta outbound, ack. Nunca foi validado ponta a ponta com o container real.
4. **W4 — Mídia WAHA**: `SendMedia` (hoje `ErrCapabilityNotSupported`) + exibição de mídia inbound na UI.
5. **M06** chatbot/automação. **D3.4–D3.8 Meta** somente após W1–W3 e nova ordem do produto.

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
- DONE: M04.3 fila inicial explícita e job de routing atômico no inbound.
- DONE: M04.4 dispatcher Outbox com sessão system transaction-local e validação usando `omnira_app`.
- Gate M04.4: PostgreSQL + `omnira_app` + NATS smoke confirmou publicação e `published_at`; migrations fresh up/down/up passaram.
- DONE: M05.1 API REST paginada da Inbox sob TenantContext (98dd6b7).
- DONE: M05.2 eventos realtime SSE com reautorização (387785f, fix de build 86b43f0).
- DONE: M05.3 UI React/Vite Inbox + Conversa, rotas e SSE (cebcfc1, 691b6f4).
- DONE: H0 saneamento (022e74f): `000020.down` seguro, adapter PG paralelo morto removido (Rotate/Update/redação portados ao canônico), `Update` de conexão agora persiste, Dockerfiles Go 1.25 + `.dockerignore`.
- DONE: M05.4 (1bab8b0) `POST /api/v1/tenants/{tid}/inbox/conversations/{cid}/assign|unassign`. Claim atômico (`FOR UPDATE`, perdedor recebe 409), RBAC `conversation.claim` (agent/supervisor/admin) e `conversation.manage` (supervisor/admin: atribuir a outro/soltar de outro; alvo precisa de `conversation.claim`), histórico `assignment_events`, audit `conversation.assigned|unassigned`, repetição idempotente = 200 `changed:false`, cross-tenant = 404 sem oráculo. Body só aceita `assignee_user_id` opcional; tenant/ator vêm do JWT+membership. Gate: 4 testes HTTP com Postgres real (-race x3) + smoke na imagem da API + vitest.
- DONE: D3.2 verificação de webhook Meta (challenge + HMAC-SHA256 + resolução por `phone_number_id`), `internal/channels/meta`.
- DONE: D3.3 parsing inbound Meta (texto/mídia/botões/status), dedupe por `wamid`, intake tenant-safe (f39829d). Gate: E2E com Postgres real + `omnira_app` (dedupe, `tenant_id` forjado ignorado, isolamento A/B). Opt-in `OMNIRA_META_ENABLED`.
- DONE: W1 API de conexão/sessão WAHA. `POST|GET /api/v1/tenants/{tid}/channels/waha/connections`, `GET .../{cid}`, `POST .../{cid}/session/start|stop`, `GET .../{cid}/qr`. Somente `channel.manage` (só `tenant_admin`; migration 000024; o RLS de escrita em `channel_connections` também exige admin). Criação exige `risk_acknowledged:true` (registrado com o ator); a chave HMAC do webhook é gerada no servidor, guardada cifrada e nunca devolvida. `start` é idempotente (cria a sessão com o webhook `OMNIRA_PUBLIC_BASE_URL/webhooks/v1/whatsapp/waha/{id}` só se faltar). `GET` atualiza e persiste o status (WORKING→active + número pareado; STOPPED→disconnected; FAILED→failed). Audit `channel.connection_created|session_started|session_stopped`. Gate: 3 testes HTTP com Postgres real + RBAC/cross-tenant/oráculo 404 (com mutação de verificação), teste opt-in contra WAHA real (`OMNIRA_WAHA_TEST_URL/KEY`: missing→create→stopped→start→needs_qr→QR→stop) e smoke da imagem da API contra WAHA real. Nota: o QR só fica disponível ~1–2 s após o start.
- TODO: D3.4 outbound Meta, D3.5 mídia (download c/ allowlist SSRF), D3.6 status outbound, D3.7 health, D3.8 templates.
- TODO: M05.5 envio outbound com idempotency key (= W2 acima).
- TODO: M06 chatbot/automation depois do vertical Inbox funcional.
