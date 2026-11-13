# CONTEXT.2 — Context Intelligence v2: desenvolvimento incremental

**Status:** FOUNDATION CODE, NOT WIRED. Não ativar em produção.
**Repositório:** `keslleykledston/20-OMNIRA`
**Branch:** `feat/context-intelligence-v2-foundation`
**Decisões existentes:** ADR-0017 (TopicThreads), ADR-0020 (memória de atendimento), ADR-0036 (frontend), ADR-0040 (acesso Hub), ADR-0048 (Headless), ADR-0049 (Service Context).
**Terminologia:** Conversation = transporte; TopicThread = assunto; service_session = atendimento; service_case/service_task = ocorrência/pendência operacional; ticket = unidade operacional existente. Não criar uma entidade concorrente para estes conceitos.

## 1. Resultado da auditoria de main (2026-11-13)

- `migrations/000063`: `topic_threads`, `message_topic_links`, `topic_conversation_links`, `topic_ticket_links` e `topic_summaries` já existem, com chaves compostas e FORCE RLS.
- `migrations/000066`: outbox `job.inbox.message_persisted.v1`, `intelligence_jobs`, lease, retries e processamento por mensagem.
- `internal/intelligence/application/summary_service.go`: resumos versionados e debounce por contagem (`FirstSummaryAt=3`, `RegenerateEvery=5`).
- `internal/intelligence/application/topic_classifier.go`: classificador em shadow mode, por mensagem.
- `internal/intelligence/application/context_builder.go`: contexto limitado e topic-scoped; níveis de verdade já implementados em `domain/context.go`.
- `internal/servicectx`: sessões, ocorrências, tarefas, timeline e contexto de cliente com seleção de conta.
- `web/src/components/inbox/ContextPane.tsx` e `web/src/components/topics/TopicsPanel.tsx`: sidebar, assuntos, resumos, Copiloto e integração ao atendimento já existem.
- Worker `apps/worker/cmd/omnira-worker/main.go` compõe pipelines, `TopicSummary` e `TopicAIRouting` por flags. UI React/Vite real — documentação mais antiga menciona Next.js; preferir implementação atual.
- Migrations até `000175` presentes em main no momento da auditoria. **Reservar número somente após atualização do checkout**; outras branches podem avançar.

## 2. O que esta branch já implementa

- `internal/intelligence/contextwindow`: política pura de flush (30s idle, 120s máximo, 12 mensagens) e triggers `manual`, `handoff`, `claim`, `finalize`, `priority`. Nenhum timer em memória ou dependência de IA.
- `internal/intelligence/contextdelta`: parser estrito de JSON de IA (limite de tamanho, campos conhecidos, alias de tópico e referências de evidências em escopo); proposta não se torna ação operacional.
- Testes unitários dos dois pacotes.

### CONTEXT.2A — Storage e agendamento (2026-11-13, implementado nesta branch)

- **Migration `000176_context_analysis_windows`** (aditiva; não toca `topic_threads`/`topic_summaries`/`intelligence_jobs`): `context_analysis_windows` (uma janela por `(tenant_id, conversation_id, generation)`, state machine `open → due → running → completed/dead`, com `failed` voltando para `due` via retry) e `context_analysis_window_messages` (associação distinta e cursor determinístico `(message_at, message_id)` — nunca `created_at` sozinho). FORCE RLS nas duas: membro do tenant só lê (mesmo split de `intelligence_jobs`); só sessão de sistema escreve. Índice parcial garante no máximo uma janela `open` por conversa; `generation` é o fencing token. Round-trip `up/down/up` provado idêntico (`scripts/test-migration-roundtrip.sh`).
- **`internal/intelligence/ports/context_window.go`** + **`internal/intelligence/adapters/context_window_store.go`** (`PostgresContextWindowStore`): `ResolveMessage` (tenant/conversa vêm da linha gravada, nunca do evento), `Touch` (estende a janela aberta da conversa ou abre uma nova; idempotente por mensagem via `ON CONFLICT`; fecha sozinha para `due` quando a política decide, sem esperar o sweep, no caso `max_messages`), `Force` (fecha imediatamente para os triggers manual/handoff/claim/finalize/priority), `Sweep` (fecha por inatividade/idade máxima, `SKIP LOCKED`, todo o sistema), `Claim` (arrenda `due`/`running` com lease vencido, `SKIP LOCKED`, fencing por `attempt` igual a `intelligence_jobs`), `MessageIDs`, `Complete`/`Retry`/`Dead`.
- **`internal/intelligence/application/context_window_runner.go`**: `ContextWindowRunner` (sweep → claim → analyze → complete/retry/dead), mesmo desenho do `JobRunner` existente. `WindowAnalyzer` é o ponto de extensão da CONTEXT.2B; `NoopWindowAnalyzer` (só CONTEXT.2A, nunca produção) completa toda janela sem analisar nada — existe só para exercitar o pipeline de agendamento ponta a ponta antes da 2B existir.
- **`internal/worker/contextwindow` (novo consumer)**: consome o MESMO evento `job.inbox.message_persisted.v1` que o roteamento já consome, mas com seu próprio consumer durável (`worker-context-window`) — um problema aqui nunca atrasa o roteamento, e vice-versa. Mensagens de grupo são ignoradas (ADR-0055 já cobre grupos; fora do escopo desta feature).
- **Flag `OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED`** (`internal/intelligence/application/flags.go`), padrão `false`. Ligada, o worker inicia o consumer + o runner (com `NoopWindowAnalyzer`) — independente de `OMNIRA_TOPIC_THREADS_ENABLED`, já que o janelamento é ortogonal à classificação de tópicos.
- **Testes**: 11 testes de integração (Postgres real, RLS A/B) cobrindo abertura/extensão/fechamento por `max_messages`, idempotência de evento redelivered, 8 abridores concorrentes da mesma conversa (sem duplicar janela aberta), isolamento entre tenants/conversas, `ResolveMessage` (inbound only, nunca outbound/inexistente), `Force` (com/sem janela aberta), `Sweep` (idempotente, só fecha o que já venceu), `Claim` com 2 workers concorrentes (nunca a mesma janela), recuperação de lease vencido (fencing por `attempt` barra o claim antigo de completar), retry/dead fenced, e ordenação determinística de `MessageIDs`. Mais 4 testes do consumer (3 unitários com fake store + 1 com JetStream real descartável provando at-least-once/idempotência/poison-message).
- **Não wireado ainda** (decisão consciente, não esquecimento): os triggers `handoff`/`claim`/`finalize` não têm ponto de chamada real em `servicectx` — `Force` já existe e está testado, só falta o call-site, que faz mais sentido junto da CONTEXT.2C (quando `POST .../context-analysis/refresh` também precisar de `Force` para o trigger `manual`).

**Não implementado ainda:** análise estruturada real (CONTEXT.2B — `NoopWindowAnalyzer` é só o scaffolding); API `context-view`; sidebar enriquecida; fluxo de confirmação de compromissos; E2E; call-sites reais de `Force` para handoff/claim/finalize; implantação. A flag `OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED` segue `false` em todo lugar — nenhuma flag foi ligada em produção.

## 3. Próximas fatias verticais necessárias antes de liberar

### CONTEXT.2A — Storage e agendamento

- Projetar migration **aditiva**, sem duplicar `topic_threads`, `topic_summaries`, `context_events` ou `service_tasks`.
- Criar `context_analysis_windows` ou equivalente, com `tenant_id`, `conversation_id`, `first_message_at`, `last_message_at`, contagem de mensagens distintas, ponteiro/cursor inclusivo, `generation`, `due_at`, state, lease, attempt, error_class e unique/índices de deduplicação apropriados.
- Usar IDs e sequência/ordem de persistência determinística; `created_at` sozinho não é cursor seguro se mensagens tiverem timestamp igual ou transações concorrentes. Documentar semântica de cutoff e reprocessamento.
- Toda tabela tenant-owned: `ENABLE/FORCE ROW LEVEL SECURITY`, políticas específicas de autorização, FKs compostas `(tenant_id,id)`, testes A/B de leitura/escrita, grants mínimos.
- Nunca confiar em `tenant_id` do webhook/evento; descobrir Tenant por mensagem persistida. Persistir envelopes só com identificadores, não payload de cliente.
- Separar roteamento por mensagem (atual e rápido) da análise semântica agrupada (nova e assíncrona). Não reter inbox/webhook enquanto uma janela aguarda.
- `SKIP LOCKED`, lease recuperável, fencing token `generation`, idempotência de commit e cap de backlog. Eventos manual/handoff/claim/finalize devem forçar a análise sem esperar inatividade.
- Para jobs enfileirados, leituras por Tenant autorizado e worker system scoping via `WithSystemTenantSession`; concorrência multiworker e crash tests.

### CONTEXT.2B — Análise estruturada

- Manter `topic_threads` como assunto único e `topic_summaries` como resumo versionado; snapshots adicionais devem referenciar versão de entrada/saída.
- Modelo retorna apenas propostas tipadas: `need`, `observation`, `commitment`, `suggested_action`, `possible_resolution`, `risk`. Cada fato tem aliases de evidência.
- Gerar aliases M1..Mn/T1..Tn **no servidor**, e só aceitar aliases do lote autorizado. Converter para IDs reais somente após validação, sem confiar em URLs/IDs do texto do modelo.
- `possible_resolution` nunca fecha automaticamente um tópico, ticket, tarefa, caso ou sessão. A IA não pode iniciar OS, alterar contato ou criar cobrança.
- Respeitar precedência `system_verified > provider_verified > customer_confirmed > agent_confirmed > ai_inferred`.
- Propostas devem ser registradas com prompt_version, model, analyzed_message_ids/range, geração, custo, latência e versão anterior; uso de IA por Tenant via ledger existente.
- Caso haja múltiplas contas de um contato e seleção indefinida, o resumo não pode resolver a conta pela inferência. Delimitar a consulta segundo `ContactContext.NeedsSelection`.
- Conversas internas/grupos: não contaminar memória operacional de consumidor; usar políticas específicas. Escopar tópico antes de construir prompts; dados de outros tópicos não entram por associação ambígua.
- A saída de IA é dado não confiável, nunca system instructions. Incorporar transcrição de mídia somente conforme flags e políticas atuais.

### CONTEXT.2C — Context View e Workspace

- Construir read model agregado **somente no servidor**, apoiado em tópicos/sessões/tarefas existentes. API versionada `GET /api/v1/tenants/{tenantID}/inbox/conversations/{conversationID}/context-view` (proposta, ajustar convenções no roteador existente).
- Resposta: `analysis_state`, `updated_at`, `coverage`, `stale`, `summary`, `topics`, `pending_actions`, `suggestions`, `evidence_refs`, `needs_customer_account_selection`. Verificar permissões `conversation.read` + `topic.read` e, para detalhes de tarefas/conta, permissões `service.read`/escopo Hub existentes. Retornar campos redigidos quando necessário; não usar só autorização visual.
- Reaproveitar `ContextPane` / `TopicsPanel`, sem copiar shell do Lovable; expandir a seção de inteligência no painel direito, com colapso, atualização manual, mensagens/evidências clicáveis e indicação de versão/staleness.
- Em Hub delegado, não acoplar leitura de contextos às regras de membership simples; passar pela resolução `EffectiveTenantContext`, concessão + contrato + permission keys, negando acesso se não autorizado.
- `POST .../context-analysis/refresh` deve ser idempotente, rate limited e registrar intenção, não aguardar IA síncrona.
- Copiloto deve permitir perguntas com citações internas e drafts, mas nunca envio automático de mensagens.

### CONTEXT.2D — Segurança, testes e rollout

- Unitários: janela com tráfego contínuo, triggers, relógio, contagem, aliases inválidos, saída malformada, estado confirmado superior.
- Integração PostgreSQL 16 real: RLS A/B, grant revogado, dois operadores concorrentes, mensagens duplicadas, mesmo timestamp, janela reaberta, lease expirado, retry, dead letter e crash após commit.
- HTTP: 403/404 para cross-tenant, scope Hub, conta desconhecida, bloqueio de roteamento e execução sem permissão.
- React: estado vazio/erro/sem IA/AI stale, operador transferido, troca de Tenant limpa cache, drawer/mobile.
- Custo: evitar solicitar inferência por mensagem; medir chamadas por conversa, p95 de resposta, tokens por Tenant, taxa de correção e falso-positivo.
- Flag específica `OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED=false` (padrão). Shadow pilot → allowlist Tenant → rollout; rollback desliga flag sem excluir dados.
- SQL migration rollback documentado (preferir forward-fix em produção); não fazer reset, DROP ou seed em produção.
- Atualizar OpenAPI/AsyncAPI, ADR novo se alterar decisão relevante, `.changes/`, `docs/AI_HANDOFF.md` se existir e relatório de gates.

## 4. Critério de conclusão (NÃO atendido nesta branch atualmente)

Só declarar FEATURE IMPLEMENTED quando worker, persistência, API e interface estiverem conectados, testados em PostgreSQL real, build Docker e frontend passarem, RLS/Hub validado, Codex review independente sem blockers, e flag desativada por default. Só declarar DEPLOYED após aplicação deliberada do release e smoke no ambiente alvo.

## 5. Orientação para o Claude Code

Continuar nesta branch (não recriar o projeto). Ler `START-HERE.md`, `AGENTS.md`, `CLAUDE.md`, `CONTEXT.md`, ADR-0017, ADR-0020, ADR-0049 e este documento. Auditoria read-only, `git status` e `git log` primeiro. Inspecionar mudanças de outras branches antes de alterar worker/rotas/migrations. Implementar as fatias restantes, com Codex como revisor independente, e somente solicitar implantação depois de todos os gates. Nunca usar merge automático nem tocar recursos de produção sem aprovação e janela operacional.
