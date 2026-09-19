# Reuse Audit — DeskcommCRM → OMNIRA

**Fase:** D0 — Audit only (nenhum código copiado nesta fase)
**Repositório doador:** https://github.com/melgarafael/DeskcommCRM
**source_commit (todas as entradas, salvo indicação contrária):** `04ef7981cba1c5f87422cf79f8ad44454e731d4c`
**Capturado em:** 2026-09-19T02:16:26Z
**Ver também:** `SOURCE-MAP.md` (tabela resumida)

Classificação: `COPY` (reaproveitável quase direto) · `ADAPT` (código com adaptação) · `PORT` (lógica traduzida para Go) · `INSPIRE` (padrão útil, não copiar implementação) · `REJECT` (incompatível com decisões OMNIRA).

---

### RLS — templates canônicos e helpers

- source_path: `docs/specs/01-spec-platform-base.md` §2.7 (helpers) e §3 (Templates A–D)
- purpose: 4 funções SQL (`fn_user_org_ids`, `fn_is_platform_admin`, `fn_user_role_in_org`, `fn_role_at_least`) + 4 templates de policy que cobrem "95% dos casos" segundo o próprio doc: isolamento CRUD simples, read-write split por role, owner-scoped, e append-only audit.
- dependencies: Postgres puro (SQL), `auth.uid()` do Supabase Auth como fonte do usuário atual.
- tenant_assumptions: toda tabela tenant-aware tem `organization_id`; usuário pode pertencer a múltiplas orgs; existe um papel "platform admin" transversal que bypassa a checagem de org.
- security_assumptions: `auth.uid()` é confiável (vem do JWT validado pelo PostgREST/Supabase); as funções são `security definer` para evitar recursão de RLS ao consultar `user_organizations` a partir de uma policy da própria `user_organizations`.
- runtime_assumptions: PostgREST expõe as tabelas diretamente ao frontend; RLS é a ÚNICA camada de autorização para leituras diretas.
- classification: INSPIRE
- omnira_target: já implementado de forma equivalente e testada em `internal/tenancy` (ver commit local de correção de RLS desta sessão: `FORCE ROW LEVEL SECURITY` + funções `SECURITY DEFINER` `has_active_membership`/`has_active_admin_membership` + `db.WithTenantSession` fazendo o papel de `auth.uid()` via `set_config('app.current_user_id', ...)`). O padrão dos 4 templates (A/B/C/D) é útil como checklist ao desenhar as próximas tabelas tenant-owned (M02 Contacts, M03 Conversations/Tickets) — mas a implementação OMNIRA não expõe Postgres direto ao frontend (não há PostgREST), então RLS é defesa em profundidade atrás da API Go, não a única camada.
- tests_to_port: o *padrão* de teste (criar 2 orgs, simular usuário via contexto de sessão, contar linhas cross-tenant = 0, contar linhas próprias >= 1) já foi portado nesta sessão para `internal/tenancy/adapters/isolation_test.go` usando `db.WithTenantSession` real contra Postgres. Vale adicionar o "controle positivo" simétrico (ver próxima entrada) que ainda não existe no OMNIRA.
- rationale: a lógica SQL não é copiável (depende de `auth.uid()`/PostgREST), mas o *desenho* — funções nomeadas e reaproveitáveis, 4 templates cobrindo a maioria dos casos, distinção owner-scoped vs. tenant-scoped — é diretamente aplicável ao desenhar as próximas policies Go/SQL do OMNIRA.

### RLS — varredura de completude (o "buraco" que uma lista fixa deixa)

- source_path: `tests/invariants/rls-completude-varredura.test.ts`
- purpose: em vez de confiar numa lista mantida à mão de "tabelas que têm teste de RLS", deriva do catálogo (`pg_class`/`pg_attribute`) TODA tabela com coluna `organization_id` e reprova qualquer uma que não apareça em uma de três listas nomeadas: `TABLES` (teste comportamental completo no arquivo irmão), `PROVA_PROPRIA` (teste comportamental em outro arquivo, citado) ou `DEBITO_CONHECIDO` (dívida herdada, fotografada, não uma aprovação).
- dependencies: Vitest + `docker exec psql` contra um Postgres efêmero de CI.
- tenant_assumptions: mesma convenção `organization_id` em toda tabela tenant-aware.
- security_assumptions: o cabeçalho do arquivo documenta um caso REAL encontrado em produção: uma tabela (`org_guardrail_layers`) tinha `relrowsecurity=true` E uma policy presente, mas a policy dizia `organization_id in (...) or true` — RLS "ligada" e "com policy" não é prova de isolamento; só simular o JWT e contar linhas cross-tenant prova.
- runtime_assumptions: nenhuma (é uma varredura de catálogo + execução de query).
- classification: INSPIRE (a lição de design), PORT (o mecanismo de varredura, adaptado a Go/SQL)
- omnira_target: `tools/check-rls` (citado no `PROMPT-CLAUDE-CODE.txt` e no `START-HERE.md` do OMNIRA como tool a criar). Proposta: uma query que lista toda tabela com `tenant_id` via `information_schema`/`pg_class`, cruza com `relrowsecurity` E `relforcerowsecurity` (o OMNIRA já teve um incidente nesta sessão exatamente da classe que este teste denuncia: RLS habilitada mas sem `FORCE`, tornando-a inerte para o owner da tabela — ver commit local "RLS estava inerte"), e falha se alguma tabela tenant-owned não estiver numa lista nomeada de "prova comportamental" ou "dívida conhecida".
- tests_to_port: o invariante central — "toda tabela tenant-aware TEM `relrowsecurity=true` E `relforcerowsecurity=true` E aparece em uma lista nomeada com teste comportamental ou dívida fotografada" — deve virar um teste Go real (`internal/platform/db` ou `tools/check-rls`), rodando contra o banco de dev/CI do OMNIRA.
- rationale: PORT porque é puramente mecânico (SQL de catálogo + comparação de listas) e generalizável a qualquer stack; o achado de design (RLS "ligada" não é RLS "efetiva") já se provou verdadeiro no próprio OMNIRA nesta sessão, o que eleva a prioridade deste item para o Wave D1 (Tenancy Harness).

### Idempotency-Key (reserva vs. recibo)

- source_path: `docs/specs/01-spec-platform-base.md` §7.3
- purpose: `idempotency_keys` com CHECK `(status_code IS NULL) = (response_body IS NULL)` — a linha nasce como "reserva" (ambos NULL, TTL curto de 60s) e vira "recibo" (ambos preenchidos, TTL 24h) só depois do efeito rodar. Uma segunda requisição concorrente colide no índice único `(organization_id, key, endpoint)` enquanto a reserva está viva, e recebe `idempotency_in_progress` (409, retentável) em vez de reexecutar.
- dependencies: nenhuma além de Postgres; é lógica pura de aplicação.
- tenant_assumptions: chave de idempotência é escopada por `(organization_id, key, endpoint)` — nunca só por `key`, senão dois tenants colidiriam.
- security_assumptions: nenhuma direta; a integridade vem do índice único + constraint de par (nunca um dos dois campos preenchido sozinho).
- runtime_assumptions: nenhuma específica de Next.js — é um padrão de handler HTTP genérico.
- classification: PORT
- omnira_target: `internal/platform/idempotency` (não existe ainda). Aplicável a qualquer endpoint de criação do OMNIRA que precise ser seguro a retry — primeiro candidato natural é `POST /api/v1/tenants/{id}/members` e, mais adiante, ingestão de webhook do WhatsApp (M06).
- tests_to_port: (a) chave repetida com mesmo body → replay da resposta gravada, sem reexecutar o efeito; (b) chave repetida com body diferente → 409 `idempotency_conflict`; (c) duas requisições concorrentes com mesma chave → uma executa, a outra recebe `idempotency_in_progress`; (d) efeito que lança → reserva "vence" imediatamente, retry subsequente executa de verdade (não fica preso).
- rationale: é lógica de banco de dados pura (uma tabela + duas constraints + um algoritmo de 4 passos), diretamente traduzível para Go/pgx sem tocar em nenhuma dependência Next.js/Supabase. Nenhuma outra fonte consultada tem uma especificação tão precisa do caso "efeito lança no meio" — vale a pena adotar literalmente.

### Cursor pagination HMAC-protected

- source_path: `docs/specs/01-spec-platform-base.md` §7.2
- purpose: cursor opaco = `base64url(JSON) + "|" + HMAC-SHA256(JSON)`, decodificado com `timingSafeEqual` e comparação de `organization_id` esperado embutido no próprio cursor (rejeita cursor de outro tenant antes mesmo de tocar o banco).
- dependencies: apenas `crypto` nativo.
- tenant_assumptions: o tenant_id vai DENTRO do cursor assinado — um cursor roubado/reaproveitado de outro tenant é detectável e rejeitado (`cursor_tenant_mismatch`) sem precisar consultar nada.
- security_assumptions: chave de assinatura de 32+ bytes fora do cursor; comparação em tempo constante.
- runtime_assumptions: nenhuma.
- classification: INSPIRE
- omnira_target: `internal/platform/pagination` já existe no OMNIRA — comparar contra este desenho (o detalhe de embutir e verificar o tenant_id DENTRO do cursor assinado é o ponto mais valioso a conferir; se a implementação atual usa cursor não-assinado ou não carrega o tenant, isso é uma lacuna de segurança sutil que vale abrir como ADR/issue).
- tests_to_port: cursor de tenant A usado numa query de tenant B → rejeitado antes da query rodar (não apenas retorna vazio — rejeita explicitamente).
- rationale: INSPIRE porque o próprio `internal/platform/pagination` já existe no OMNIRA e não deve ser substituído às cegas; a captura de valor aqui é uma auditoria pontual (o item já é uma tarefa concreta, não um port de código).

### Error codes canônicos

- source_path: `docs/specs/01-spec-platform-base.md` §7.5
- purpose: tabela de ~30 códigos de erro padronizados (`validation_error`, `auth_required`, `forbidden_role`, `idempotency_conflict`, `conversation_already_claimed`, etc.) com HTTP status e "quando" de cada um, incluindo uma "nota de reconciliação" que resolve sinônimos divergentes entre specs antigas (`unauthenticated` vs `auth_required`).
- dependencies: nenhuma.
- tenant_assumptions: nenhuma direta.
- security_assumptions: nenhuma direta.
- runtime_assumptions: nenhuma.
- classification: INSPIRE
- omnira_target: `docs/architecture/API-GOVERNANCE.md` — vale alinhar um vocabulário canônico de error codes agora, antes que o OMNIRA acumule divergências como as que este documento teve que reconciliar. Achado concreto desta sessão que reforça a prioridade: o handler de login mock do OMNIRA (`internal/platform/authn/http_handler.go`) usava `http.Error` (texto puro) em vez de um código estruturado — já corrigido nesta sessão para JSON `{error, message}`, mas ainda sem um vocabulário fechado de códigos.
- tests_to_port: nenhum teste específico; é uma tabela de referência para o design da API.
- rationale: o valor é o *processo* (uma tabela única, versionada, com nota de reconciliação para desvios históricos) mais do que os códigos específicos, que são do domínio Deskcomm (leads, pipelines) e não se aplicam 1:1 ao domínio OMNIRA (tenants, tickets, hub).

### ChannelAdapter / ChannelProvider (contrato de canal)

- source_path: `lib/channels/types.ts`
- purpose: interface `ChannelAdapter` com um único método obrigatório de fato (`resolveRecipient`, `isConfigured`, `send`, `codes`) e ~8 métodos OPCIONAIS (`fetchProfilePictureUrl`, `checkHealth`, `sendTemplate`, `fetchInboundMedia`, `signalTyping`, `echoExternalIds`, `resolvePhoneForIdentity`, `resolveRegisteredPhone`) — o chamador testa a PRESENÇA do método em vez de perguntar "qual provider é isto", o que um lint próprio do repo (`lint:channels`) proíbe.
- dependencies: nenhuma externa; é só TypeScript de tipos + um `OutboundMedia` importado de `lib/waha/media-send` (acoplamento que o próprio comentário do arquivo assinala como algo a desfazer quando WAHA for "absorvido pela Fase 3").
- tenant_assumptions: `ChannelTenantScope { organizationId }` é obrigatório em toda operação — o comentário do código documenta um bug real de produção (issue #236) causado por um campo opcional que permitia resolver credencial sem checar a organização, resultando em mensagem saindo pela conta errada.
- security_assumptions: nenhuma property do tipo carrega segredo; a resolução de credencial é responsabilidade de quem implementa o adapter, nunca do tipo.
- runtime_assumptions: nenhuma (é um contrato TypeScript, roda em qualquer runtime Node).
- classification: PORT
- omnira_target: `internal/channels/` (M06 WhatsApp Official do roadmap OMNIRA). O prompt mestre do OMNIRA já pede exatamente isto: `ChannelProvider` com `VerifyWebhook`, `ParseInbound`, `SendText`, `SendMedia`, `HandleDeliveryStatus`. Este contrato do Deskcomm é mais granular e maduro (métodos opcionais testados por presença, capabilities declarativas separadas do adapter) e serve de base direta para desenhar a interface Go.
- tests_to_port: (a) `ChannelTenantScope.organizationId` nunca opcional — o equivalente Go é o tipo não compilar sem o campo, não um teste de runtime; (b) qualquer teste que force dois providers com o mesmo identificador externo (`sessionRef`) em organizações diferentes e prove que a resolução de credencial nunca mistura os dois.
- rationale: é o item de maior valor de PORT direto no pacote inteiro — a lição sobre "capability opcional testada por presença" e "tenant scope obrigatório e não-opcional" são diretamente traduzíveis para uma interface Go (`type ChannelAdapter interface { ... }` com métodos que retornam `(T, bool)` para capacidades opcionais, ou um segundo tipo `ChannelCapabilities` estático).

### Meta Cloud API adapter (comportamento)

- source_path: `lib/channels/adapters/meta-cloud.ts`
- purpose: adapter "burro" (só traduz formato) para a WhatsApp Cloud API oficial da Meta — resolve destinatário para E.164 em dígitos puros, monta payload por tipo de mensagem (texto/mídia/contato), decide `isConfigured()` sempre `true` (a decisão real é dentro de `send()`, documentado como correção de um bug de produção onde mensagens ficavam presas silenciosamente), valida saúde via `GET /{phone_number_id}`, baixa mídia recebida com uma **allowlist de host por sufixo de domínio** (`.fbsbx.com`, `.fbcdn.net`) explicitamente para prevenir SSRF.
- dependencies: `fetch` nativo, `resolveMetaCreds` (busca credencial por sessão OU por env, nessa ordem), `createAdminClient` (Supabase service role).
- tenant_assumptions: credencial é resolvida por `(organizationId, phoneNumberId)` — nunca por um único número global; existe fallback de env para instalações legadas com um único número.
- security_assumptions: (1) SSRF — a URL de mídia devolvida pela Graph API é validada contra allowlist de sufixo de host + protocolo `https:` antes de qualquer download; (2) token nunca é logado; (3) `isConfigured()` sempre `true` é uma decisão deliberada e documentada, não um descuido — a alternativa (checar env síncrono) causou uma classe de bug real (mensagens `queued` silenciosamente).
- runtime_assumptions: nenhuma além de `fetch`/timeout via `AbortSignal.timeout`.
- classification: PORT
- omnira_target: `internal/channels/meta/` — comportamentos a portar, em ordem de prioridade: (1) a allowlist de host por sufixo para download de mídia (proteção SSRF explícita no `ACCEPTANCE-GATES.md` do kit); (2) o padrão de erro "lança em vez de devolver `{externalId: null}`" para credencial ausente, que faz o chamador classificar corretamente `queued`/`meta_not_configured` em vez de fingir sucesso; (3) `checkHealth` reaproveitando a MESMA chamada usada para validar credencial, distinguindo `reachable: false` (erro de rede — não é o canal que caiu) de `status: "FAILED"` (token/permissão realmente inválidos).
- tests_to_port: (a) URL de mídia com host fora da allowlist → rejeitada antes do download; (b) protocolo não-https → rejeitado; (c) credencial ausente em `send()` → erro classificável como "não configurado", nunca um "sucesso" fantasma; (d) `checkHealth` com timeout de rede → `reachable: false`, nunca confundido com "canal desconectado".
- rationale: é comportamento de integração de alto risco (SSRF, dinheiro/reputação do número, mensagens perdidas silenciosamente) já testado em produção real segundo os comentários do código — vale mais a pena portar o COMPORTAMENTO linha a linha do que reinventar do zero em Go, mesmo que a sintaxe mude por completo.

### Inbox — rotas e componentes React

- source_path: `app/app/inbox/` (page.tsx, [id]/page.tsx, loading.tsx) e `components/inbox/*.tsx` (24 arquivos: ConversationList, ChatThread, Composer + subpasta `composer/` com AttachMenu/AudioRecorder/TemplateMenu, CRMSidePanel, media/* para renderização de anexos)
- purpose: layout operacional de 3 colunas (lista de conversas | thread | contexto do CRM) com colapso para navegação de uma coluna em mobile, atalhos de teclado (`InboxKeyboardShortcuts.tsx`), indicadores de janela de 24h fechada (`JanelaFechadaAviso.tsx`, `JanelaSelo.tsx`), reatribuição (`ReassignDialog.tsx`), snooze (`SnoozeButton.tsx`).
- dependencies: Next.js App Router, Supabase Realtime (`postgres_changes`/broadcast) via hooks não lidos nesta amostra (`docs/specs/04-spec-pipeline-attendance.md` §4 documenta `useRealtimeChannel`), shadcn/ui.
- tenant_assumptions: toda query de conversas/mensagens é implicitamente escopada por RLS (o componente não filtra `organization_id` manualmente — confia na sessão Supabase).
- security_assumptions: nenhuma decisão de autorização no componente — delega inteiramente a RLS/API.
- runtime_assumptions: Server/Client Components do Next.js App Router; realtime via WebSocket do Supabase.
- classification: ADAPT
- omnira_target: `apps/web/features/inbox/` (Next.js do OMNIRA, ainda não construído — o frontend anterior em Vite+React foi descartado nesta sessão por divergir do domínio real do MVP). A estrutura de 3 colunas e a lista de componentes (ConversationList/ChatThread/Composer/CRMSidePanel) é diretamente reaproveitável como ESQUELETO de componente, mas cada um precisa: trocar toda chamada Supabase por chamada à API Go do OMNIRA; substituir Supabase Realtime por SSE/WebSocket próprio (o `docs/architecture/FRONTEND-UX.md` do OMNIRA já define R0.2 como "shell de atendimento" com o mesmo layout conceitual); aplicar o Omnira iOS Design System (tokens, componentes) em vez de shadcn/ui puro.
- tests_to_port: nenhum teste de comportamento aqui (é camada de apresentação); vale portar os *casos de estado* documentados nos nomes dos componentes — janela de 24h fechada, snooze, reassign — como especificação de UX para M05 Realtime Inbox.
- rationale: ADAPT porque o valor está na estrutura/composição de componentes e nos estados de UX já mapeados (24h window, snooze, reassign, atalhos de teclado), não no código React em si, que está inteiramente amarrado a Supabase e precisa ser reescrito na troca de data layer.

### Claim atômico ("Eu cuido" / AT-02)

- source_path: `docs/specs/04-spec-pipeline-attendance.md` §9
- purpose: atribuição de conversa sem dono via `UPDATE conversations SET assigned_to_user_id = $caller WHERE id = $conv AND assigned_to_user_id IS NULL RETURNING *` — se 0 linhas voltam, a API responde 409 `conversation_already_claimed` com o nome de quem já assumiu.
- dependencies: nenhuma além de SQL puro; o frontend usa React Query com `onError` tratando o 409 especificamente.
- tenant_assumptions: a query já roda dentro do escopo RLS da organização (implícito, não mostrado no SQL do resumo).
- security_assumptions: a atomicidade vem inteiramente do predicado `WHERE ... IS NULL` na mesma instrução UPDATE — não há SELECT-then-UPDATE (que teria race condition).
- runtime_assumptions: nenhuma.
- classification: PORT
- omnira_target: `internal/bpo` — qualquer operação de "assumir ticket sem dono" no OMNIRA (M03 Conversations+Tickets, M04 Queues+Routing) deve seguir este padrão de UPDATE condicional atômico em vez de "ler estado, decidir em Go, escrever" (que teria a mesma race condition que este design evita).
- tests_to_port: teste de corrida — dois goroutines tentando o mesmo claim simultaneamente, exatamente 1 sucesso e 1 falha com código de conflito (o próprio kit cita isto como gate obrigatório do Wave D5: "race condition claim test + audit").
- rationale: é um padrão de banco de dados puro (uma instrução SQL + tratamento de 0-rows-affected), sem nenhuma dependência de stack — PORT direto e de baixo custo, alto valor (elimina uma classe inteira de bug de concorrência).

### Roteamento / fila (manual + round-robin, visibilidade)

- source_path: `docs/specs/13-spec-governanca-atendimento.md` §5
- purpose: dois modos no MVP do Deskcomm — `manual` (default) e `round_robin` (opt-in); "elegível" = disponível ∧ dentro do horário ∧ abaixo da capacidade; fila ordenada por `last_inbound_at` ASC (quem espera há mais tempo primeiro), com "posição" derivada do índice na lista.
- dependencies: `event_log` + worker consumindo `conversation.routing_requested` com dedup at-least-once.
- tenant_assumptions: `visibility_mode` (`own_and_unassigned` default) é uma configuração por organização.
- security_assumptions: nenhuma decisão de autorização nova — reaproveita o RBAC existente (role `agent`).
- runtime_assumptions: cron/worker consumindo tabela de eventos, não fila de mensagens dedicada.
- classification: INSPIRE
- omnira_target: `internal/bpo` (M04 Queues+Routing do OMNIRA já lista exatamente "manual assignment, round-robin, department-based queue" como MVP routing — coincide). A mecânica de consumo assíncrono deve ser NATS JetStream + Outbox no OMNIRA, não `event_log`+cron.
- tests_to_port: fila reordenada corretamente por `last_inbound_at` mesmo quando `last_message_at` muda (o doc cita um bug real, #464, causado por usar a coluna errada para ordenar); "sem elegível ⇒ fila com backoff" como caso de borda a cobrir.
- rationale: a *regra de negócio* (elegibilidade, ordenação da fila, modos manual/round-robin) é diretamente aplicável ao M04 do OMNIRA já planejado; o *mecanismo* de transporte (event_log+cron) é REJECT — o OMNIRA já decidiu NATS JetStream+Outbox para o mesmo problema.

### Métricas por responsável (TME/TMA equivalente)

- source_path: `docs/specs/13-spec-governanca-atendimento.md` §6
- purpose: fórmulas exatas e testáveis para "funil por owner" (snapshot), "leads ganhos/perdidos por owner" (janela sobre `closed_at`), "conversas atendidas por assignee" (janela sobre `assigned_at`), e principalmente "tempo até 1ª resposta" — com a definição precisa de excluir resposta de bot/IA (`sent_by_user_id IS NOT NULL` como discriminador humano vs. bot) e de descartar casos onde a resposta precede a primeira mensagem inbound.
- dependencies: nenhuma além de SQL de agregação; o doc nota que a autorização por role é feita pela PRÓPRIA RLS da tabela de origem, não por uma checagem paralela na função de métricas.
- tenant_assumptions: toda subquery filtra `organization_id` explicitamente, nunca do corpo da requisição.
- security_assumptions: a função de agregação roda `SECURITY INVOKER` (não `DEFINER`) precisamente para que a RLS do chamador se aplique — um `agent` agregando vê só os próprios números "de graça", sem lógica extra de filtro.
- runtime_assumptions: índices parciais dedicados por métrica (documentados com `EXPLAIN ANALYZE` como prova de ausência de seq scan sob RLS ativa).
- classification: PORT (a especificação, não o SQL Supabase-specific)
- omnira_target: `internal/bpo` (M10 Supervisor do OMNIRA — TME/TMA já é objetivo explícito do release R0.5/M10). A definição de "tempo até 1ª resposta excluindo bot" é diretamente reaproveitável assim que o OMNIRA tiver mensagens com marcação de autor humano vs. automação.
- tests_to_port: TTFR não conta resposta de bot; TTFR descarta conversas onde a resposta precede o primeiro inbound (iniciada pelo atendente); janela semiaberta `[from, to)` aplicada de forma consistente.
- rationale: fórmulas de métricas operacionais são um dos ativos mais "portáveis" do pacote — não dependem de nenhuma peça de stack Deskcomm-specific, só da existência das colunas equivalentes (assigned_at, closed_at, sent_by_user_id) no schema OMNIRA.

### Invariantes de governança executáveis com "gap conhecido" (`it.fails`)

- source_path: `docs/specs/13-spec-governanca-atendimento.md` Apêndice A
- purpose: suíte de testes onde uma lacuna de implementação conhecida é registrada como `it.fails` com comentário `GAP(Gx)` — o teste passa ENQUANTO o gap existe, e vira falha obrigatória (quebrando o build) no exato commit que corrigir o gap, forçando quem corrigiu a "flipar" o teste para o modo normal. Funciona como uma "catraca": nunca deixa a dívida regredir silenciosamente nem esquece de promover o teste quando a dívida é paga.
- dependencies: Vitest (`it.fails`); o padrão é framework-agnóstico.
- tenant_assumptions: nenhuma.
- security_assumptions: nenhuma direta, mas o mecanismo é usado para RASTREAR dívida de segurança/isolamento sem escondê-la.
- runtime_assumptions: nenhuma.
- classification: INSPIRE
- omnira_target: `docs/delivery/DEFINITION-OF-DONE.md` e a suíte de testes Go do OMNIRA. O framework de testes Go padrão não tem `it.fails` nativo, mas o padrão é replicável com `t.Skip("GAP(Gx): ...")` documentado, OU com um teste que roda mas cujo `t.Fatalf` é convertido em log até uma flag ser removida — o importante é a DISCIPLINA (gap nomeado, rastreável, que vira falha obrigatória quando resolvido), não a sintaxe.
- tests_to_port: nenhum teste específico — é uma técnica de organização de testes a adotar ao lidar com dívida técnica conhecida (o próprio commit desta sessão sobre RLS documentou "pendências conhecidas" em texto; formalizar como teste catraca seria uma melhoria direta).
- rationale: é uma prática de engenharia de testes independente de stack, e resolve exatamente o problema que apareceu nesta sessão (RLS "documentada como pendência" em vez de "testada como pendência que expira").

### Rule engine (automation) — gatilho → condição → ação

- source_path: `lib/automation/engine.ts`, `lib/automation/types.ts`, `lib/automation/actions/*.ts`, `lib/automation/conditions.ts`
- purpose: motor que consome eventos do `event_log`, avalia `conditions` (JSON declarativo) contra um `context` hidratado a partir do `entity_kind`/`entity_id` do evento (lead, contato, appointment, mensagem), e executa uma lista de `actions` registradas (add-tag, assign-owner, call-webhook, send-whatsapp, send-ai-message, create-or-move-lead, start-message-flow). Tem guarda anti-loop (eventos causados por uma regra não disparam a mesma regra de novo) e um hook `postponeUntil` que adia o EVENTO INTEIRO antes de qualquer ação rodar (evita execução parcial em retry).
- dependencies: Supabase client (admin, bypassa RLS — cada lookup filtra `organization_id` manualmente, documentado como doutrina "service role sem filtro manual é anti-pattern").
- tenant_assumptions: `organizationId` vem do evento, nunca do payload de configuração da regra.
- security_assumptions: admin client bypassa RLS por design (é um worker, não uma requisição de usuário) — a barreira de tenant é 100% manual (`.eq("organization_id", org)` em toda query), o que o próprio `CLAUDE.md` do Deskcomm lista como anti-pattern nº 10 quando feito incorretamente (aqui é feito propositalmente e de forma consistente).
- runtime_assumptions: cron/worker consumindo `event_log`, registry de actions plugável.
- classification: PORT
- omnira_target: `internal/automation/` (M07 Chatbot/Automation do roadmap OMNIRA). A separação `ActionExecutor` (interface com `execute` + `postponeUntil` opcional) e o registry de actions é um desenho limpo, direto de portar para uma interface Go equivalente, com o consumo trocado de `event_log`+cron para NATS JetStream+Outbox (decisão já congelada do OMNIRA).
- tests_to_port: anti-loop (evento causado por regra não redispara a mesma regra); `postponeUntil` adia o evento inteiro atomicamente (nenhuma ação parcial executa antes do adiamento); guard de `entity_kind` evitando processar o mesmo evento lógico duas vezes por causa de trigger duplicado.
- rationale: a arquitetura de motor de regras (trigger→condição→ação, com registry plugável e anti-loop) é independente de linguagem e mapeia bem ao domínio M07/M08 (Tool Runtime) do OMNIRA — só o transporte (event_log+cron → NATS+Outbox+worker Go) precisa mudar.

### Follow-up engine (workflow/graph de reengajamento)

- source_path: `lib/followup/engine.ts` (963 linhas), `lib/followup/graph-schema.ts` (739 linhas), `lib/followup/node-handlers.ts`
- purpose: motor de "flows" versionados (grafo de nós) para sequências de follow-up/reengajamento — inscrição (`enroll`), avanço por gatilho (`gatilho-etapa`, `gatilho-caso`, `gatilho-presenca`), detecção de silêncio (`silence-sweep`), planejamento de tempo (`timing-plan`, `plano-de-tempo`), e reatividade (voltar ao fluxo quando o contato responde depois de "dormente").
- dependencies: Supabase, `event_log`.
- tenant_assumptions: fluxo pertence a uma organização; inscrição (`enrollment`) referencia contato+fluxo dentro do mesmo tenant.
- security_assumptions: `validate-publish.ts` sugere validação de grafo antes de publicar (nós órfãos, ciclos, etc. — não confirmado sem leitura completa).
- runtime_assumptions: worker/cron para avançar o tempo (`timing-plan`), não um scheduler dedicado.
- classification: INSPIRE
- omnira_target: `internal/automation/` (mesma área do rule engine — M07). Sobreposição conceitual forte com `AutomationFlow`/`AutomationVersion`/`AutomationRun` já especificados no `PROMPT-CLAUDE-CODE.txt` do próprio OMNIRA (M07, nodes MVP: Message/Condition/CaptureInput/SetVariable/ToolCall/HumanHandoff/End) — mas o Deskcomm modela um domínio mais específico (funis de vendas com "no-show", "reativação", "modelos" por nicho como clínica) que não é o MVP do OMNIRA agora.
- tests_to_port: nenhum item específico priorizado nesta fase — volume grande (34+ arquivos) demais para portar sem que o M07 do OMNIRA primeiro decida seu próprio schema de nós, que já diverge do Deskcomm (o OMNIRA já teve essa decisão tomada de forma independente no `PROMPT-CLAUDE-CODE.txt`).
- rationale: INSPIRE e não PORT porque o M07 do OMNIRA já tem uma spec própria de nodes (Message/Condition/CaptureInput/SetVariable/ToolCall/HumanHandoff/End) definida ANTES desta auditoria — reescrever com base no grafo do Deskcomm arriscaria herdar complexidade de um domínio (funis de vendas B2C, clínicas, no-show) que não é o do OMNIRA (atendimento BPO multi-tenant). Vale revisitar quando M07 for implementado, como checklist de edge cases (reatividade dormente, timing plan, silence sweep), não como fonte de schema.

### Skills / harness do próprio Deskcomm

- source_path: `.agents/skills/deskcomm-instalar`, `deskcomm-cliente-novo`, `deskcomm-metricas`, `deskcomm-prompt`, `deskcomm-contribuir`, `deskcomm-doutrina`, `sistema-vivo`
- purpose: skills operacionais para instalar/atualizar em VPS, configurar cliente por nicho, tunar prompt de agente de IA, checklist de contribuição espelhando a triagem, doutrina condensada ("as três regras que mais custam, antes de escrever código"), e um "Living System Checklist" (toda feature precisa ter entrada+saída, emitir log/atividade, ter porta de navegação, mecanismo anti-morte, declarar seu laço de retorno).
- dependencies: nenhuma técnica — são documentos Markdown consumidos por agentes de IA (Claude Code/Codex/Cursor).
- tenant_assumptions: N/A.
- security_assumptions: N/A.
- runtime_assumptions: N/A.
- classification: INSPIRE
- omnira_target: `.agents/skills/` do próprio OMNIRA (já referenciado em `docs/delivery/AGENT-IMPLEMENTATION-PROTOCOL.md`). O "Living System Checklist" (item 13 do Definition of Done do Deskcomm) é o achado mais valioso desta seção: a exigência de que toda feature nova declare "o que muda no sistema quando ela erra" (o laço de retorno) é uma pergunta que o `DEFINITION-OF-DONE.md` atual do OMNIRA não faz explicitamente.
- tests_to_port: N/A (são práticas de processo, não testes de código).
- rationale: puramente processual — útil como inspiração para refinar `docs/delivery/DEFINITION-OF-DONE.md` e as skills existentes do OMNIRA, sem nenhuma dependência de código a portar.

### Doutrina geral (AGENTS.md / CLAUDE.md do Deskcomm)

- source_path: `AGENTS.md`, `CLAUDE.md`
- purpose: documento de convenções "não negociáveis" cobrindo multi-tenancy, idempotência, API REST, auth/RBAC, audit log (incluindo um caso REAL de tabela de auditoria que parecia append-only mas não era, por causa de `ALTER DEFAULT PRIVILEGES` do Supabase — corrigido só na migration 0258), LGPD, packaging/distribuição (uma imagem serve todas as marcas, nunca build na VPS do cliente), extensões declarativas, e uma seção extensa e brutalmente honesta sobre "armadilhas" de rodar a suíte de testes (grep que conta casos vs. arquivos, exit code divergente do rodapé por erro não tratado).
- dependencies: N/A (é doutrina, não código).
- tenant_assumptions: `organization_id NOT NULL` em toda tabela tenant-aware é tratado como não-negociável, com um anti-pattern nomeado para FK-como-string (`owner_email` em vez de `owner_user_id uuid`).
- security_assumptions: "service role usado em request handler sem filtrar `organization_id` manualmente" é anti-pattern nº 10 explicitamente proibido — mesma classe de erro que o rule engine automation acima evita corretamente.
- runtime_assumptions: N/A.
- classification: INSPIRE
- omnira_target: comparar contra `CLAUDE.md`/`AGENTS.md` do próprio OMNIRA e `docs/delivery/DEFINITION-OF-DONE.md`. Dois achados concretos de alto valor: (1) a doutrina DIRC ("Duplicar/Integrar/Referenciar/Calcular" — pergunta a fazer antes de adicionar qualquer campo novo a uma tabela) não tem equivalente explícito no OMNIRA; (2) a seção sobre "gate escolhido não é suíte" (typecheck/lint verdes não provam que a suíte de testes passa) é um lembrete útil dado que esta própria sessão descobriu bugs (RLS inerte, `causation_id` inexistente, `string(rune(N))` corrompendo métricas) que compilavam e passavam em testes existentes sem serem pegos.
- tests_to_port: N/A.
- rationale: é a fonte mais densa de "lições aprendidas em produção real" do pacote inteiro — não há código a portar, mas há disciplina de engenharia diretamente aplicável, e vários dos "achados de produção" citados (ex: função `security definer` nova nasce exposta a `anon` por DUAS origens de GRANT distintas) são exatamente a classe de bug sutil que uma auditoria de RLS como a desta sessão deve continuar procurando no OMNIRA.

---

## Sumário por prioridade

### P0 (reforça diretamente o que está em construção agora — M01/Wave D1 Tenancy Harness)

- **Varredura de completude de RLS** (`tools/check-rls`): o achado mais crítico desta auditoria é que o padrão de teste do Deskcomm (varredura de catálogo + duas listas de exceção nomeadas) teria pego, de forma automatizada, o EXATO bug que esta sessão encontrou manualmente no OMNIRA (RLS habilitada sem `FORCE`, tornando-a inerte para o owner da tabela). Prioridade máxima para o Wave D1.
- **Templates de RLS (A/B/C/D)**: o OMNIRA já implementou o equivalente ao Template A (isolamento simples) via `has_active_membership`. Falta o equivalente aos Templates B (read-write split por role) e C (owner-scoped) para quando M02/M03 chegarem com tabelas onde nem todo membro pode escrever.
- **Idempotency-Key**: nenhum equivalente existe hoje no OMNIRA; é pré-requisito de qualquer endpoint de escrita exposto a webhook (M06 WhatsApp) ou a cliente HTTP não confiável.

### P1 (Meta WhatsApp Cloud + Inbox — Wave D3/D4)

- **Meta Cloud adapter**: forte em SSRF (allowlist de host por sufixo), tratamento de credencial ausente (lança em vez de fingir sucesso), e health check reaproveitando a chamada de validação. Fraco/ausente na amostra lida: não implementa envio a grupos (`resolveRecipient` retorna `null` para `isGroup`), e a API de templates de mensagem (`sendTemplate`, `ChannelTemplateOps`) está no CONTRATO (`types.ts`) mas o comportamento de sincronização (`template-sync.ts`, `contract-hash.ts`) não foi lido em detalhe nesta fase — recomendo uma segunda passada de leitura focada nesses dois arquivos antes do Wave D3 real.
- **Claim atômico**: padrão de UPDATE condicional pronto para portar tal como está; baixo custo, alto valor, remove uma classe inteira de race condition do M04.
- **Inbox components**: esqueleto de componentes e estados de UX (janela 24h, snooze, reassign) reaproveitáveis como especificação de layout para M05, mas TODO o código precisa ser reescrito (Supabase → API Go, shadcn puro → Omnira iOS Design System).

### P2 (Automation, métricas, doutrina de processo — Wave D6/D7/D8)

- **Rule engine (automation)**: arquitetura limpa (trigger→condição→ação, registry plugável, anti-loop) diretamente portável para M07/M08, trocando o transporte para NATS+Outbox.
- **Métricas por responsável (TME/TMA)**: fórmulas prontas e testáveis para M10 Supervisor, sem dependência de stack.
- **Follow-up engine**: INSPIRE, não PORT — o M07 do OMNIRA já tem sua própria spec de nodes, divergente do domínio do Deskcomm (funis de venda B2C vs. atendimento BPO). Revisitar como checklist de edge cases, não como fonte de schema.
- **Doutrina/skills**: sem código a portar, mas dois achados de processo valem incorporar ao `DEFINITION-OF-DONE.md` do OMNIRA: doutrina DIRC (antes de adicionar campo) e "Living System Checklist" (toda feature declara seu laço de retorno).

---

## Riscos e achados mais importantes para o Wave D0→D1 (Tenancy Harness)

1. **A auditoria já pagou seu custo antes mesmo de terminar**: o padrão de varredura de completude de RLS do Deskcomm (`rls-completude-varredura.test.ts`) descreve, no próprio cabeçalho, a MESMA classe de vulnerabilidade que esta sessão encontrou manualmente no OMNIRA horas antes (RLS "ligada" + policy presente ≠ isolamento provado; a causa raiz no OMNIRA era ainda mais grave — a role de conexão era superuser com bypassrls, então nem FORCE bastava sem trocar a role). Isso eleva a prioridade de portar essa varredura para `tools/check-rls` de "boa ideia" para "gate obrigatório antes de qualquer tabela tenant-owned nova".
2. **RLS como única camada é uma escolha do Deskcomm que o OMNIRA não deve replicar sem ajuste**: o Deskcomm expõe Postgres direto ao frontend via PostgREST, então RLS é a ÚNICA barreira de autorização para leitura. O OMNIRA tem uma API Go entre o frontend e o banco — RLS deve continuar como defesa em profundidade (o que a arquitetura já assume corretamente), mas isso significa que os testes de "prova comportamental" do Deskcomm (simular JWT direto contra Postgres) precisam de um equivalente que teste TANTO a camada de aplicação Go QUANTO o banco isoladamente, não apenas um dos dois.
3. **O padrão "service role bypassa RLS, filtro manual obrigatório" é uma superfície de risco que o OMNIRA vai reencontrar**: assim que o M07/M08 (automation/tool runtime) precisar de um worker Go que opere fora do contexto de uma requisição de usuário (equivalente ao `admin client` do rule engine Deskcomm), a mesma disciplina — todo lookup cross-entity filtra `tenant_id` explicitamente, nunca confia em RLS sozinha quando a conexão bypassa RLS por design — precisa ser codificada como convenção testada, não como lembrete em comentário.
