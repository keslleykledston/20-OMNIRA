# 01 — Arquitetura (Service Context & Operations)

Registro formal: `docs/adr/0049-service-context-and-operations.md`. Este documento detalha as decisões; o código é a fonte de verdade da implementação atual (ver `00-audit.md`).

## 1. Princípios

1. **Camada, não sistema novo.** Conversa, mensagem, roteamento, Hub, Flow Builder, IA e `tickets` permanecem. O novo núcleo (`internal/servicectx`) liga-se a eles por chaves compostas `(tenant_id, id)`.
2. **Uma só regra de negócio**, no serviço de aplicação; Workspace, API e fluxos chamam o mesmo serviço (ADR-09). O frontend só renderiza.
3. **Estado atual ≠ histórico** (ADR-07): entidades guardam o estado; `context_events` guarda o que aconteceu. Resumo de IA nunca é fonte de verdade.
4. **Estados independentes** (ADR-10): nenhuma transição cruzada implícita. A única exceção é *declarada e testada*: **Finalizar** (ADR-0020) fecha a conversa **e** a sessão ativa — isso já era o significado de "finalizar atendimento".
5. **Aditivo e reversível**: só tabelas novas (+ CHECK de `permission_domains`, permissões e uma chamada opcional no `Finalize`). Tudo atrás de flags; `down.sql` por migration.

## 2. Modelo (resumo; colunas em `04-data-model.md`)

```
conversations 1 ──< service_sessions >── (N:M) service_cases
                         │  └──< service_tasks >── (opcional) service_cases
                         └──< context_events (timeline)      external_records ──< service_external_links (exatamente 1 alvo)
messages (inalterada)  ← ligada à sessão por INTERVALO [opened_at, closed_at) da mesma conversa
```

Tabelas novas: `service_counters`, `service_categories`, `service_resolution_codes`, `service_sessions`, `service_cases`, `service_case_sessions`, `service_tasks`, `context_events`, `external_records`, `service_external_links`, `external_record_requests`.

## 3. Atendimento (`service_sessions`) e a ADR-0020

- **Conversa = canal de comunicação. Sessão = unidade operacional.** Uma conversa pode ter várias sessões *em sequência* (nunca simultâneas): índice parcial único `(tenant_id, conversation_id)` onde `status IN ('open','in_progress','waiting')` garante **no máximo uma ativa**; o serviço serializa com `SELECT … FOR UPDATE` da conversa, a mesma trava usada por `Finalize` e pelo motor de fluxos.
- **Quando uma sessão "efetivamente inicia"?** Não em toda mensagem (isso repetiria o ticket-placeholder: ver `memory/ticket-placeholder-per-conversation`). Nasce por **ação explícita**, de forma idempotente (`EnsureActive`): (a) operador/API abre; (b) primeira ação de serviço do operador (classificar, criar tarefa/ocorrência, vincular OS); (c) nó de fluxo *Iniciar atendimento* (**ainda não existe** no catálogo de 26 tipos; será criado na Fase 6 com efeito idempotente por `(tenant, run_id, seq)`); (d) **Finalizar** quando ainda não há sessão (o atendimento puramente informativo ganha protocolo no ato de encerrar, em uma transação). Abertura automática ao assumir conversa fica fora da v1 (toca roteamento, área em disputa com a branch headless).
- **Conversa de grupo / interna** (`conversation_kind <> 'customer_service'`): sem sessão (mesma regra de `Finalize`).
- **Compatibilidade com a ADR-0020:** `Finalize` continua fechando a conversa (inalterado). Com a flag ligada, na **mesma transação** ele chama a porta opcional `SessionCloser`: garante a sessão e a fecha com o resultado mapeado do motivo (`resolved`→derivado dos vínculos: OS→`external_service_order`, ocorrência→`case_created`, tarefa→`task_created`, senão `information_provided`; `transferred`→`transferred`; `no_response`→estado `abandoned`/`abandoned`; `duplicate|spam|other`→`other`). Sem flag: nada muda.
- **Encerrar a sessão sem encerrar a conversa** é capacidade nova (`POST …/close`): útil para API/CRM e fluxos; a próxima ação de serviço abre outra sessão (outro protocolo) na mesma conversa. No Flow Builder isso será um nó **próprio e novo** (`close_service_session`, Fase 6) — distinto do nó existente `close_conversation`, que continua finalizando a conversa (e, com o hook, fecha também a sessão).
- **Transferência** não é estado nem cria protocolo. O histórico "quem segurou quando" continua em `assignment_events` (roteamento) e entra na linha do tempo **por projeção em leitura**, sem hook no roteamento e sem cópia.
- **Responsável atual** = `conversations.assigned_to_user_id` (lido por junção); a sessão guarda `opened_by_user_id` e `closed_by_user_id`. Desvio consciente do campo `assigned_agent_id` do pedido: duplicar o responsável criaria duas fontes de verdade (ADR-07/ADR-03).
- **Mensagens não são copiadas nem marcadas.** Mensagem pertence à sessão se `messages.conversation_id = sessão.conversation_id` e seu `created_at` cai dentro de um **episódio** da sessão (`service_session_episodes`, `[started_at, ended_at)`, meio-aberto: um instante pertence a exatamente um lado). Uma sessão tem **um** episódio até ser reaberta; reabrir inicia **outro episódio**, então o tempo em que ficou fechada **não é absorvido** (as mensagens desse intervalo ficam sem sessão) e o primeiro trecho não muda. Invariantes (gatilho + índice): no máximo uma sessão ativa por conversa e uma sessão só abre depois do `closed_at` da anterior, portanto os episódios de sessões diferentes da mesma conversa são disjuntos. **Fonte de tempo única:** `opened_at`, `closed_at` e os limites dos episódios vêm do relógio do banco (`clock_timestamp()`), nunca do servidor de aplicação. **Limitação aceita e medida:** `messages.created_at` é o `now()` da transação de ingestão (início da transação); uma mensagem cuja transação começou um instante antes da abertura da sessão mas confirmou depois é classificada como "anterior à sessão" (erro limitado à duração de uma transação de ingestão, milissegundos). Mensagens fora de qualquer episódio são "ainda não classificadas" por definição. Alternativas rejeitadas: coluna `messages.session_id` (escrita no caminho quente de entrada, backfill, conflito com a branch headless que altera `messages`); tabela de relação por mensagem (N linhas por mensagem, duplicação de dado).
- **Contato não identificado:** com conversa, a sessão herda `contact_id` da conversa (conversa de cliente sempre tem contato; "não classificado" é o `conversation_kind='unclassified'` do contato, que não impede a sessão). `customer_id` (conta) é opcional; só vira obrigatório por regra de negócio explícita futura. Atendimento **sem conversa** (iniciado pela API, ex.: ligação) é permitido: `conversation_id` NULL, `contact_id` opcional, `origin_channel` é um token `[a-z0-9_]{1,40}` (padrão: o provedor da linha quando há conversa, senão `other`).
- **Protocolo:** `service_counters (tenant_id, kind) → last_value`; alocação por `INSERT … ON CONFLICT DO UPDATE SET last_value = last_value + 1 RETURNING` na **mesma transação** que insere a sessão. Serializa por tenant (trava de linha), não usa `MAX+1`, não colide, e um rollback devolve o número (sem lacunas por falha; lacunas só por exclusão manual, que não existe). Ordem fixa de travas: conversa → contador (evita deadlock). Formato `AT-<n>`, início configurável (padrão 10000). `UNIQUE (tenant_id, protocol)` e `UNIQUE (tenant_id, protocol_sequence)`. Unicidade é **por tenant**; o UUID segue como chave. **Protocolos legados/importados**: nunca ocupam a faixa gerada; ficam em `external_protocol` (texto, único por tenant quando presente) e `protocol_origin = 'imported'`; o `protocol` visível continua gerado. Nenhum protocolo histórico é inventado para as 180 conversas já encerradas (continuam sem sessão).
- **Estados:** `open → in_progress | waiting | closed | abandoned`; `in_progress ↔ waiting`; `in_progress|waiting → closed | abandoned`; `closed|abandoned → in_progress` (**reabertura**, ver abaixo). Qualquer outra transição é `409`. Transferência: nenhum estado.
- **Reabertura** (decisão desta ADR, restrita): somente se (i) a conversa ainda está **aberta** (a ADR-0020 mantém conversa fechada como final), (ii) a sessão é a **mais recente** da conversa, (iii) não existe outra sessão ativa nela, (iv) dentro da janela `reopen_window` (72 h, padrão), (v) com motivo. Incrementa `reopen_count`, mantém o protocolo, **inicia um novo episódio** (não amplia o anterior), registra evento; o resultado anterior fica no evento (histórico), não se perde. **Precedência explícita entre fechar e reabrir:** `Open`/`EnsureActive` **nunca** reabre — depois de fechada, a próxima abertura cria **outra sessão com outro protocolo**; só o endpoint `reopen`, chamado de propósito por quem tem `service.close`, devolve a mesma sessão ao trabalho.
- **Encerramento exige resultado.** `resolution_code` referencia `service_resolution_codes` (por tenant) cujo `canonical_code` pertence ao conjunto fechado `information_provided, resolved_first_contact, task_created, case_created, external_service_order, transferred, abandoned, other`. Os 8 canônicos existem para todo tenant como linhas padrão (semeadas ao primeiro uso, `system=true`); o tenant adiciona códigos próprios que **apontam** para um canônico (relatórios agregam pelo canônico). O resultado é a **classificação principal**; ações adicionais (tarefa, ocorrência, OS) são **vínculos**, nunca o campo de resultado.
- **Primeira resposta** (`first_response_at`): menor `created_at` de mensagem `outbound` com `sent_by_user_id` não nulo dentro do intervalo; calculado na leitura de sessão ativa e **gravado no encerramento** (relatórios).

## 4. Linha do tempo (`context_events`)

- Append-only (sem UPDATE/DELETE para `omnira_app`, gatilho contra UPDATE, como `conversation_closures`). Colunas de contexto (`session_id`, `case_id`, `contact_id`, `customer_id`, `conversation_id`) com índices para as três leituras reais: por sessão, por ocorrência, por contato.
- Payload: JSON versionado (`schema_version`), **só ids, enums e contagens**, teto de 2 KB, validado em Go por struct tipada (sem campo de texto livre). Texto de mensagem **nunca** entra; mensagem é referenciada por id.
- Gravado pelo serviço, na mesma transação da mudança de estado. `dedup_key` opcional (único por tenant) torna a gravação idempotente em retentativas.
- Leitura = `context_events` ∪ projeção de `assignment_events` (virtual, `source_system='omnira.routing'`), ordenada por `(occurred_at, id)` com cursor *keyset*; sem `OFFSET`.
- **Sem emissão ao `outbox_events`** (decisão após revisão independente): hoje o publisher só tem stream para `job.*` e o banco real só contém eventos `job.*`; um evento de domínio publicado em `events.*` não teria destino e queimaria retentativas. Além disso, a política de INSERT do outbox exige membro, Hub com `can_reply` ou gestor — um delegado de Hub só com `service.*` ficaria bloqueado, e contornar isso com uma função `SECURITY DEFINER` abriria um caminho de escrita fora da autorização. `context_events` **é** o diário durável (envelope versionado `{event_id, tenant, type, version, occurred_at, correlation_id, causation_id, entity, payload}`); a **entrega** a terceiros (webhook assinado, cursor, dead-letter) reaproveita o diário/entregador da branch headless quando ela for integrada — dependência registrada em `06-events-webhooks.md`, não entregue aqui.

## 5. Ocorrências e tarefas

- `service_cases`: estados `open, investigating, waiting, resolved, closed, cancelled`. Resolver exige texto de resolução; cancelar exige motivo. **Reabertura**: `resolved → investigating|open` permitida (motivo + evento + `reopen_count`); `closed` e `cancelled` são finais (abre-se outra ocorrência e vincula). `case_number` por contador (`OC-<n>`). Vínculo N:M com sessões (`service_case_sessions`, `relation ∈ origin|reference`); **nunca** criada automaticamente por conversa.
- `service_tasks`: estados `open, in_progress, done, cancelled`. Origem **validada por integridade, não por par solto**: colunas `session_id`, `case_id`, `flow_run_id` (FKs compostas) + `origin_type` com CHECK que exige exatamente a combinação coerente (`session`→`session_id`; `case`→`case_id`; `flow`→`flow_run_id`; `human|ai|api`→qualquer vínculo opcional). Concluir tarefa **não** altera a ocorrência. Cancelar exige motivo. Tarefa de IA nasce **apenas por aprovação** (gateway: `pending_approval`).
- **`follow_up_items` convive.** Continuam sendo promessas/pendências *do contato* registradas ao finalizar (ADR-0020), com tela própria. Tarefa é a entidade operacional. Unificação (visão única "pendências do cliente") é leitura agregada em `customer-context`, não migração de dados.

## 6. Registros externos (ERP/CRM)

- `external_records`: espelho **mínimo** (sistema, tipo, id externo, status externo, `sync_state`, `last_synced_at`, `version`); único por `(tenant_id, integration_id, external_type, external_id)`. O ERP é a autoridade (ADR-06): o OMNIRA **não** cria OS própria; guarda referência, projeção e histórico.
- Vínculos em `service_external_links` (exatamente um entre sessão/ocorrência/tarefa, CHECK; único por par).
- **Solicitar OS** = `external_record_requests` (tentativa durável: `in_flight → confirmed_success | confirmed_failure | outcome_unknown`, `idempotency_key` único por tenant, `request_hash`), o mesmo modelo de `ticket_external_create_attempts`. **Timeout ≠ falha**: estado `outcome_unknown` **bloqueia nova tentativa** até reconciliar (consulta ao ERP por chave de correlação); só `confirmed_failure` libera retry. Porta `ServiceOrderGateway` (Get/RequestCreate/Status); sem adaptador configurado para o tenant → `503 not_configured` (nunca simula sucesso). Adaptador real depende de contrato do provedor (K3G não tem API de OS conhecida: **não inventada**); entregamos o adaptador *fake* de contrato e os testes de falha.
- `tickets` continua o espelho do chamado do ERP para o painel existente. Um ticket externo pode ser também `external_record(type='ticket')` — sem migração de dados.

## 7. Segurança (resumo; detalhe em `07-rls-security.md`)

- Sete permissões novas: `service.read/create/update/close/assign/link_external/request_external`. Escrita no banco exige `actor_has_permission` (mais estrito que o padrão operacional), leitura exige membership ativa ou delegação.
- **Hub:** novo domínio `service` em `permission_domains` (leitura: `service.read`; escrita: create/update/close/assign/link_external); `service.request_external` **nunca** delegável (ação em sistema externo, cf. ADR-0040 §"nunca delegável"). Políticas `*_read_delegated`/`*_write_delegated` + restritiva `*_acting_one_instance`, com a visibilidade da conversa/contato herdada (contrato limitado a filas não alcança as demais). Rotas marcadas `Delegable`.
- **IA:** ferramentas novas de leitura livres; qualquer escrita (criar tarefa, ocorrência, solicitar OS) = `pending_approval`; `service.request_external` jamais executável por IA (somente humano com a permissão, após aprovação explícita).
- Webhook/ERP de entrada nunca escolhe o tenant: o tenant vem da credencial/integração armazenada; o `external_id` é sempre resolvido **dentro** do tenant.
- Jobs assíncronos releem o tenant da linha persistida (`WithSystemTenantSession` + predicado explícito `tenant_id`).

## 8. Flags

`OMNIRA_SERVICE_CONTEXT_ENABLED` (rotas e serviço; padrão **false**), `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED` (Finalize cria/fecha sessão; padrão false), `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED` (solicitar OS; padrão false), `OMNIRA_SERVICE_AI_TOOLS_ENABLED` (ferramentas de IA; padrão false). Declaradas em `config.go` e em `docker-compose.yml`.

## 9. Alternativas rejeitadas

| Alternativa | Por que não |
|---|---|
| Reusar `tickets` como atendimento | `tickets` é projeção do ERP (ADR-0013); misturar recriaria o help desk concorrente e herdaria o placeholder por conversa. |
| Reusar `topic_threads` como atendimento | Assunto lógico que atravessa conversas, sem protocolo, com ciclo próprio e IA propondo; semântica oposta. |
| Reusar `follow_up_items` como tarefa | Por contato, só nasce ao finalizar, sem estados de execução nem origem; estender quebraria o contrato da ADR-0020. |
| `messages.service_session_id` | Escrita no caminho quente + backfill + conflito com a headless. |
| `MAX(protocolo)+1` | Condição de corrida; proibido. |
| Sessão automática por mensagem | Repete o ticket-placeholder; infla métricas. |
| Criar OS própria do OMNIRA | Viola ADR-06 (ERP é a fonte de verdade). |
| Novo motor de webhooks | Já existe na branch headless; duplicar viola "não duplicar". Dependência registrada. |
