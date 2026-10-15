# 00 — Auditoria do repositório (Fase 0)

Data: 2026-10-15 · Branch: `feat/omnira-service-context-operations` · Baseline: `main` @ `26e4f01` (limpo, sem alterações pendentes; o commit desta fase é `d9cdfd4`, **só documentos** — a implementação das fases seguintes é trabalho posterior e não faz parte do que a Fase 0 declara).
Método: leitura do código real (migrations, Go, web) e consultas **somente leitura** ao banco de desenvolvimento. Nenhum teste foi tratado como PASS sem rodar; esta fase não altera código.

## 1. Git, isolamento e trabalhos paralelos

| Item | Achado |
|---|---|
| Árvore principal | `main`, limpa. A implementação roda numa **worktree própria** (`/data/cafegpt/tmp/wt-svcctx`), nada da árvore principal é tocado. |
| Worktrees de outros trabalhos | `feat/admin-console`, `feat/brand-logo-favicon`, `feat/headless-crm-dual-operation`, `feat/menu-idle-reminders`, `feat/open-notice`, `feat/sector-scheduling-overflow`, `fix/backend-build-regressions`, `feat/flow-builder`. Nenhuma é alterada por este trabalho. |
| **Colisão de migration** | `main` já usa `000137_queue_capacity_no_limit`. A branch headless usa `000137`–`000141` (renumerada contra um `main` anterior) e **vai precisar renumerar de novo**. Este trabalho reserva a faixa **`000150`–`000159`** (o runner `tools/migrate-sql.sh` aplica por ordem lexicográfica e registra cada versão; lacunas são seguras). |
| Colisão de ADR | `main` termina na 0047; a headless usa 0048. Este trabalho usa **ADR-0049**. |
| Arquivos quentes compartilhados com a headless | `internal/platform/httpserver/server.go`, `internal/platform/config/config.go`, `apps/api/cmd/omnira-api/main.go`, `apps/worker/cmd/omnira-worker/main.go`, `internal/audit/domain/event.go`, `internal/attendance/*`, `internal/flows/*`. Estratégia: **mudanças aditivas e pequenas** nesses arquivos (uma linha de registro, uma constante), para o merge ser trivial. |

## 2. O que já existe e se sobrepõe ao pedido

| Conceito do pedido | Existe hoje | Fonte | Decisão |
|---|---|---|---|
| Atendimento | **Conversa fechada + `conversation_closures`** (ADR-0020): finalizar fecha a conversa; a mensagem seguinte abre conversa nova. 1 closure por conversa, imutável. | `000086`, `000087`, `internal/attendance` | **Camada nova** `service_sessions` por cima; ver §3 e `01-architecture.md` |
| Protocolo | **Não existe** (nenhuma sequência, contador ou coluna). O único "protocolo" mostrado ao cliente é o nº do chamado do ERP (`ticket_notice.go`). | auditoria | Novo: contador transacional por tenant |
| Timeline | **Não existe** tabela de eventos por atendimento. Existem `assignment_events` (quem segurou a conversa), `audit_events` (auditoria técnica), `message_topic_links`. | `000015`, `000003` | Novo `context_events` (append-only); reaproveita os existentes por **referência**, não por cópia |
| Ocorrência (caso) | **Não existe.** Parente distante: `topic_threads` (assunto lógico N:N com conversas, ADR-0017). | `000063` | Novo `service_cases`; **não** reutiliza `topic_threads` (outro significado, outro ciclo de vida, outro dono) |
| Tarefa | **Não existe** como entidade. Parente: `follow_up_items` (pendência/promessa do **contato**, só nasce ao finalizar, sem `in_progress`, sem vínculo a ticket/tópico). | `000086` | Novo `service_tasks`; `follow_up_items` **convive** (ver `01-architecture.md` §6) |
| OS / chamado externo | `tickets` (projeção local 1:1 do chamado do ERP, ADR-0013) + `ticket_external_create_attempts` / `ticket_external_status_attempts` (tentativas idempotentes, estados `in_flight/confirmed_*/outcome_unknown`) + tela de reconciliação. Conector `TicketingConnector` só para K3G (GetTicket, CreateTicket, UpdateTicketStatus). | `000014`, `000045`, `000047`, `000050`, ADR-0013 | **Não é substituído.** `external_records` generaliza o vínculo (várias OS por atendimento/ocorrência, tipos além de ticket) |
| Cliente / contrato | `customer_accounts` (ADR-0018, empresa/conta) + `contact_account_links`. **Não há entidade "contrato do cliente"** no OMNIRA. "Contrato" aparece só como `hub_tenant_service_contracts` (contrato Hub↔tenant, outra coisa). | `000077`, `000093` | `customer_id` → `customer_accounts`; contrato do cliente é **registro externo** (`external_records.external_type = 'contract'`) |
| Setor / departamento | `sectors` e `queues` (ADR-0043: fila = setor). | `000127` | `department_id` → `queue_id` (FK composta) |
| Catálogo de classificação | Não existe para atendimento. | — | Novos `service_categories` e `service_resolution_codes` (por tenant, com código canônico) |
| Auditoria | `audit_events` + `AuditService.RecordEvent` | `000003`, `internal/audit` | Reutilizado (ações novas = constantes aditivas) |
| Outbox / eventos | `outbox_events` + publisher. O publisher tenta publicar **todo** evento não publicado (`subjectForEvent`: `job.*` vai pelo próprio nome, os demais por `events.<tipo>.<agregado>`), mas o único stream é `OMNIRA_JOBS` (`job.>`). **No banco real só existem eventos `job.*`** (verificado: 8 tipos, todos `job.`). Um evento de domínio não-`job.` não teria stream e queimaria as retentativas. Não há webhook de saída em `main`. | `000003`, `worker/publisher/publisher.go:163`, `worker/jobsstream/config.go:45` | **Não emitir `service_*.v1` no outbox.** O diário durável é `context_events` (mesmo envelope versionado); a **entrega** a terceiros fica com o motor de webhooks da branch headless (ver §6) |
| Idempotência | Padrão `Idempotency-Key` `^[A-Za-z0-9._:-]{8,128}$` + `request_hash`, repetido módulo a módulo (sem helper único). | várias | Mesmo padrão, coluna `idempotency_key`+`request_hash` por entidade |
| IA | Gateway fechado de 8 ferramentas (`ToolRegistry`); escrita pedida por IA vira `pending_approval`; `ai_tool_calls` permanente. | `000069`, `internal/intelligence` | Ferramentas novas **de leitura** livres; **escrita só como `pending_approval`** |
| Flow Builder | 26 tipos de nó; efeitos locais rodam na mesma transação do passo; `create_ticket`/`close_conversation` existem. | `internal/flows` | Novos nós seguem o procedimento de 9 passos (ver `10-flow-builder-ai.md`) |

## 3. Descoberta crítica: conflito entre a ADR-01 do pedido e a ADR-0020

- **ADR-0020 (aceita, implantada, 2026-10-06):** "Finalizar atendimento" **fecha a `Conversation`**; o próximo contato abre **outra** conversa. Reabrir foi explicitamente descartado. Hoje, portanto, **1 conversa ≈ 1 episódio de atendimento**.
- **ADR-01 do pedido:** "uma conversa pode conter vários atendimentos".
- **Resolução** (detalhada em `01-architecture.md` §3): o novo modelo **suporta N sessões por conversa** (nunca impõe 1:1), mas **não altera** o comportamento da ADR-0020: Finalizar continua fechando a conversa e **passa a fechar também a sessão ativa** na mesma transação. Sessões adicionais na mesma conversa nascem de ações explícitas (`API`, nó de fluxo, ação do operador "encerrar atendimento e continuar a conversa") — comportamento novo, opt-in, atrás de flag.
- Nada de reabrir **conversa**. A reabertura de **sessão** (janela curta, só se não houver outra ativa) é decisão nova e própria desta ADR.

## 4. Dados e escala (banco de desenvolvimento, leitura)

3 tenants · 266 conversas · 4.240 mensagens · 258 tickets (87 abertos, 3 com `external_ticket_id`) · 180 `conversation_closures` · 0 `follow_up_items` · 0 `topic_threads` · 236 contatos · 9 `customer_accounts` · 16.356 `outbox_events`.
`messages` **não é particionada**; extensões instaladas: `plpgsql`, `uuid-ossp`, `pgcrypto` (**sem `btree_gist`** — não usar exclusion constraint). Escala pequena: não há necessidade de `CREATE INDEX CONCURRENTLY` hoje, mas as migrations usam `lock_timeout 5s` como as recentes.

## 5. Tenancy / RLS / Hub — o que as novas tabelas precisam seguir

- **Não existe GUC de tenant.** O isolamento é por `has_active_membership(tenant_id, current_user_id())` (e `is_system_admin()`), com contexto de sessão assinado (ADR-0042). Padrão por tabela: `ENABLE`+`FORCE` RLS, 4 políticas `<t>_{read,insert,update,delete}_tenant`, `GRANT … TO omnira_app`, FK composta `(tenant_id, x_id)` com `UNIQUE (tenant_id, id)` no pai, gatilho `forbid_tenant_change()` (`000131`).
- Teste de varredura `internal/platform/db/rls_completeness_test.go` **quebra o build** se uma tabela com `tenant_id` não tiver RLS+FORCE+política.
- **Granularidade:** hoje o RLS das tabelas operacionais não distingue papéis; a capacidade fina (`ticket.create`…) vive na aplicação (`actor_has_permission`/`PermissionChecker`). Para as tabelas novas, **escrita no banco exige `actor_has_permission(...)`** (padrão de `000130`), o que é mais estrito que o padrão operacional e atende ao requisito "read/create/update/close/assign/link_external/request_external_action".
- **Hub:** o agente do Hub **não é membro**. Entra por `hub_memberships → hub_tenant_service_contracts → effective_access_grants`; `lock_served_tenant` fixa uma instância; leitura via políticas `delegated_tenants(domínio, 'read')`, escrita via `delegated_tenants(domínio,'write')`, sempre com a restritiva `*_acting_one_instance`. `permission_domains.domain` tem CHECK fechado (`conversation, media, contact, ticket, flow, ai`) → um domínio novo `service` exige `ALTER` do CHECK. Rota só é alcançável por delegado se marcada `Delegable(...)`.
- **Permissões existentes** relevantes: `conversation.*`, `ticket.read/create/update/reconcile`, `contact.*`, `account.*`, `attendance.history`, `followup.resolve`, `topic.*`, `flow.*`. Nenhuma cobre sessão/ocorrência/tarefa.
- **Sessão de sistema** (worker, fluxos) usa `WithSystemTenantSession` e **contorna o RLS** (`is_system_admin()` está em toda política); a defesa ali é o predicado `tenant_id = $1` no código + o tenant lido da linha persistida, nunca do payload. Mantido: todo SQL novo filtra `tenant_id` explicitamente.
- Funções `SECURITY DEFINER`: o teste de catálogo `internal/platform/db/definer_hardening_test.go` verifica **apenas** `SECURITY DEFINER` + `search_path` fixo (não verifica `REVOKE EXECUTE FROM PUBLIC` nem o `GRANT` explícito — correção da revisão independente). Este trabalho **não cria função `SECURITY DEFINER`** na Fase 1 (as proteções são políticas RLS e gatilhos de linha com `SECURITY INVOKER`); qualquer função futura seguirá a convenção completa (`search_path`, `REVOKE ... FROM PUBLIC`, `GRANT ... TO omnira_app`) e ganhará asserções próprias.

## 6. Branch `feat/headless-crm-dual-operation` (leitura apenas)

Entrega identidade de aplicação externa (bearer `omn_ext_…`, escopos), controle tipado da conversa, **diário durável `domain_events`** (7 tipos, CHECK fechado), **webhooks assinados** (HMAC v1, backoff, dead-letter, SSRF guard), API de escrita coordenada com `external_command_receipts` (idempotência por operação). **Não modela** sessão/ocorrência/tarefa. Reuso planejado (pós-merge, **sem duplicar**): autenticação bearer + escopos (novos escopos exigem `ALTER` do CHECK de `scopes`), `external_command_receipts`, diário e entrega de webhook (exige `ALTER` dos CHECKs de `event_type`/`aggregate_type` e `webhook_subscriptions.event_types`). Até lá, as rotas de service-context usam a autenticação de tenant existente e **os eventos `service_*.v1` ficam no outbox**; isso é registrado como dependência, não como entrega.

## 7. Lacunas e riscos identificados

1. `tickets_active_conversation_uq` (um ticket ativo não escopado por conversa) e `conversation_closures UNIQUE(tenant_id, conversation_id)` limitam o modelo antigo; o novo não os usa como fonte de verdade de sessão.
2. O ticket-placeholder por conversa (criado em `inbound.go`) é lixo operacional conhecido. **Não** criar sessão automaticamente em toda mensagem de entrada (repetiria o erro): "atendimento efetivamente iniciado" é definido em `01-architecture.md` §3.
3. K3G não oferece sincronização incremental nem sabe-se de API de OS: **não inventar** adaptador de OS; entregar porta + tabelas de tentativa/reconciliação + adaptador fake testado.
4. Hot paths (`inbound.go`, roteamento, `messages`) têm trabalho paralelo (headless altera ~40 arquivos): este trabalho **não os toca**; ligação mensagem↔sessão é por intervalo, sem coluna em `messages`.
5. `docs/flows/STATUS.md` ainda diz "17 nós" (obsoleto; são 26).
6. Dois conjuntos de OpenAPI/AsyncAPI (`contracts/` e `docs/api`, `docs/async`) precisam ficar em sincronia — a diferença entre eles **não foi verificada** nesta fase.
7. Não verificado nesta fase: se todas as rotas de `flows`/`attendance` estão na matriz Hub (`HUB-DELEGATION-TABLE-MATRIX.md`); será checado ao marcar rotas `Delegable`.

## 8. Ferramentas e revisão independente

`go` e `docker` presentes; `codex` presente em `/home/suporte/.npm-global/bin/codex` (revisor somente leitura; **o ambiente do Codex não tem `go`**, então testes são rodados por mim e o Codex lê o código). Testes Go rodam em `golang:1.25` com banco descartável `omnira_test_*` (nunca `omnira_dev`).
