# ADR-0044 — Motor central de roteamento: setores, calendários, escalas, plantões, SLA e transbordo em cadeia

Status: **ACEITA E IMPLANTADA em LAB (2026-10-13)** — fases 1–8 entregues. Mesclada em `main` (`1f30046`, base `c804344`), enviada ao GitHub e implantada por ordem do dono; flag `OMNIRA_ROUTING_ENGINE_ENABLED` ligada com `OMNIRA_ROUTING_ENGINE_SINCE` (só conversas novas). Decisões do §12 aprovadas pelo dono em 2026-10-13 (padrões adotados). Estado do projeto: `LAB`; sem `PRODUCTION_READY`.
Origem: pedidos do dono de 2026-10-13 (módulo de horários/plantões/transbordo e *addendum* do motor central). Relaciona: ADR-0043 (fila = setor, §0), ADR-0019 (Flow Builder), ADR-0038/0040 (Hub), ADR-0042 (contexto de sessão), ADR-0018 (decisão num ponto só).
Vocabulário de evidência: IMPLEMENTED / UNIT / POSTGRES / HTTP / E2E VERIFIED / NOT WIRED / BLOCKED.
Revisão independente (Codex) do desenho e da Fase A: 0 CRITICAL, 4 HIGH de **desenho** e 4 MEDIUM; os 4 MEDIUM de código estão corrigidos; os 4 HIGH estão resolvidos neste texto (§6, §7, §8) e viram testes obrigatórios das fases 4–5.

## 0. Reconciliação com o ADR-0043 (importante)
O ADR-0043 decidiu "a fila é o setor" e disse que uma entidade `Setor` só se justificaria se um setor contivesse **várias filas**. O *addendum* afirma exatamente isso (princípios 1–3: "um setor pode possuir múltiplas filas"). Portanto: **cria-se `sectors`**; `queues` ganha `sector_id` (nulo permitido: fila antiga continua funcionando). Nada é substituído nem apagado:
- Os grupos Comercial/Financeiro/Suporte (migration 123) ganham, por migração de dados, **um setor homônimo** e ficam vinculados a ele; a fila **Default** não é ligada a setor (continua a fila de triagem).
- `RouteNew`, o rodízio atômico (`AssignRoundRobin`), `ClaimUnassigned`, a varredura de vida e a regra de entrada por canal (ADR-0043 S2) **permanecem**; o motor passa a ser o chamador deles (§5), em fases.

## 1. Diagnóstico de reaproveitamento (auditoria de 2026-10-13, código de `main`)

| Necessidade | Existe | Lacuna |
|---|---|---|
| Agenda | só o nó `business_hours` com agenda **embutida** (`internal/flows/domain/hours.go`, fuso IANA, janelas por dia, sem virar a meia-noite, sem exceções); o código admite "a plataforma não tem módulo de horário" | calendário operacional, feriados, exceções, suspensão, 24x7, herança do tenant, fuso padrão do tenant (não há coluna de fuso em `tenants`) |
| Escala/plantão | `queue_members.available` + `capacity`, `agent_profiles.status` | escala individual, plantão, contingência |
| Presença | Valkey, `tenants.routing_require_presence`, rodízio com presença | correto: só combinar, nunca confundir |
| Capacidade | `queue_members.capacity` + contagem de abertas na própria consulta | esgotar capacidade não dispara nada |
| Distribuição | `AssignRoundRobin` atômico (`FOR UPDATE SKIP LOCKED`), `ClaimUnassigned`, `assignment_events`, varredura `routing_retry_at`, pools do Hub | só rodízio e manual; sem "menor carga"; sem destino alternativo; regra duplicada em `flows/adapters/effects_postgres.go` (`assignQueueSQL` espelha `RouteNew`) |
| Timers | `flow_runs.wait_until` + `OnTimeout`; `routing_retry_at` | sem timer de espera na fila / SLA / abertura; sem retomada documentada |
| SLA | `tenants.wait_warn/danger_minutes` — **só cor de tela** | prazo, risco, violação, calendário útil |
| Auditoria/histórico | `assignment_events`, `audit_events`, mensagens ligadas à conversa | decisão de roteamento, laço, idempotência por ação |
| Hub | transferência delegada entre quem pode responder a instância; `hub_inbox_items` por escopo de fila do contrato | Hub como **destino** de transbordo, validado por contrato |

**Reutilizar:** `AssignRoundRobin`/`ClaimUnassigned` (extraídos em serviço de distribuição, mesma SQL), `assignment_events`, `outbox_events`+NATS, `audit_events`, a varredura `routing_retry_at`, `queue_members`, presença do Valkey, `hub_reach()`/`has_active_hub_access`, `flow_runs.wait_until`, `internal/scheduling` (Fase A).
**Substituição só com auditoria:** `assign_queue`/`human_handoff`/`business_hours` do Flow Builder continuam válidos e passam a delegar ao motor/calendário **sem mudar o JSON dos fluxos publicados** (compatibilidade testada com os 5 fluxos de produção).

## 2. Entidades e invariantes (tudo por tenant, RLS FORCE, FK composta `(tenant_id, …)`)

| Entidade | Papel | Invariantes |
|---|---|---|
| `sectors` | setor (nome, ativo, calendário, política de SLA, política de transbordo) | `UNIQUE (tenant_id, name)`; desativar não apaga; nome pode mudar, **id é estável** (fluxos referenciam o id) |
| `queues.sector_id` | fila do setor | FK composta; nula = fila avulsa legada; várias filas por setor |
| `queues.mode` | `manual` \| `round_robin` \| `least_loaded` (novo) | `CHECK`; `least_loaded` = menor nº de conversas abertas atribuídas, desempate por `last_assigned_at` |
| `calendars` + `calendar_exceptions` + `calendar_holidays` + `calendar_suspensions` | calendário operacional (definição validada por `scheduling.Schedule`) | fuso IANA obrigatório; sem sobreposição de exceções; `revision` incrementa em toda mudança; `tenants.default_calendar_id` (herança: setor sem calendário usa o do tenant; fila sem calendário usa o do setor) |
| `agent_work_calendars` | **escala individual** do agente (por fila ou setor), também um `scheduling.Schedule` | independente do calendário do setor; ausência = "sem restrição de escala" (comportamento de hoje) |
| `on_call_rosters` + `on_call_periods` | plantão e equipes de contingência | período com `starts_at/ends_at` (instantes), agente ativo no tenant, sem sobreposição do mesmo agente no mesmo roster; plantão pode ter capacidade própria |
| `routing_engine_state` | estado de roteamento **da conversa** (uma linha por conversa) | PK `(tenant_id, conversation_id)`; `episode`, `step_index`, `visited[]` (chaves canônicas), `hops`, `entered_queue_at`, `generation`; **uma atribuição operacional principal** continua sendo `conversations.assigned_to_user_id` |
| `overflow_policies` + `overflow_steps` | cadeia de transbordo (escopo tenant/setor/fila, mais específico vence) | etapas ordenadas, `max_steps` (padrão 4, teto 8), destino tipado (§6), `on_exhausted` explícito |
| `routing_engine_timers` | prazos duráveis | `UNIQUE (tenant_id, conversation_id, episode, kind, step_index)`: **um** timer lógico por coisa; `state` (`pending→leased→done|cancelled`), `lease_until`, `attempts`, `generation` |
| `routing_engine_decisions` | histórico auditável | `UNIQUE (tenant_id, conversation_id, episode, step_index, trigger)`: repetir o gatilho **não cria** outra decisão; retentativa incrementa `attempts` na mesma linha |
| `sla_policies` + `conversation_sla` | metas e relógios (§7) | precedência fila > setor > tenant; relógios por métrica, nunca reiniciados por transferência automática |

## 3. O motor central (`internal/routing/engine`)
Um único ponto de entrada:

```
Decide(ctx, Request) -> Decision
Request  { TenantID, ConversationID, Source(channel|flow|ai|api|manual|timer), RequestedSector/Queue, Priority, Trigger, Now }
Decision { Kind: ASSIGN_AGENT | ENQUEUE | OVERFLOW | WAIT_UNTIL_OPEN | EXECUTE_FLOW | ESCALATE | REQUIRE_MANUAL_ACTION,
           Destination, Reason, Wake, DecisionID }
```
Passos, **numa só transação** (contexto de tenant do sistema com o `tenant_id` lido do estado armazenado, nunca de payload): (1) travar `routing_engine_state` `FOR UPDATE` (cria se faltar); (2) se já existe decisão para `(episode, step, trigger)` devolver a mesma; (3) resolver setor/fila e calendário (herança); (4) avaliar elegibilidade; (5) escolher a decisão; (6) aplicar o efeito (atribuir pelo **mesmo SQL** do rodízio, enfileirar, criar/cancelar timer, ou pedir fluxo); (7) gravar `routing_engine_decisions` + `assignment_events` + `audit_events` + `outbox_events`. Handlers HTTP, webhooks, workers, Flow Builder e React **não decidem**: pedem ao motor. Quem muda de dono usa compare-and-set (`assigned_to_user_id IS NOT DISTINCT FROM <esperado>`).

**Elegibilidade (única função):** calendário do destino aberto ∧ membro ativo da fila ∧ `available` ∧ escala individual aberta (se houver) ∧ presença (se `routing_require_presence`) ∧ capacidade livre ∧ (plantão ativo, quando o destino é plantão). Nenhuma atribuição automática fora de escala/offline/sem capacidade, **exceto** etapa de contingência com flag `allow_off_shift` explícita e permissão `routing.override` (auditada).

## 4. Calendários (Fase 3 — domínio IMPLEMENTADO, persistência pendente)
`internal/scheduling` (UNIT VERIFIED, NOT WIRED): 24x7 / comercial / personalizada; vários intervalos por dia; turnos que cruzam a meia-noite (pertencem ao **dia em que começam**); exceções e feriados (exceção > feriado > semanal); **suspensão emergencial** por instantes (vence tudo, corta turno em andamento); fuso IANA; horário de verão (lacuna empurra para frente, repetição = primeira ocorrência, durações sempre entre instantes); `IsOpen`, `NextOpen`, `NextClose`, `BusinessElapsed`, `AddBusiness` (calendário útil). Fuso padrão `America/Manaus` é decisão do **adaptador** (`scheduling.DefaultTimezone`), nunca do domínio. Revisão do Codex: lookback para fusos que pularam um dia civil (Pacific/Apia) corrigido, validação em todo ponto de entrada, fronteira do horizonte definida.

## 5. Integração com o que existe (sem duplicar)
- `RouteNew` e o `assign_queue` do fluxo passam a chamar `Decide` (Fase 4) — hoje há duas cópias do mesmo SQL.
- `routing_retry_at` e a varredura continuam como rede de segurança; `routing_engine_timers` cobre o que tem **prazo** (espera, SLA, abertura do expediente).
- **Abertura/fechamento do expediente:** timer `open_wakeup` no próximo `NextOpen` do calendário do destino; ao disparar, reenfileira (limite por lote) as conversas **sem dono** em `WAIT_UNTIL_OPEN`. Ao fechar, conversas sem dono seguem o gatilho `after_hours` da cadeia; conversa com dono não muda (o atendente termina).
- **Salvar uma agenda** incrementa `revision` e, na **mesma transação**, cancela (`generation++`) e recria os timers das conversas afetadas — nada espera por um horário que deixou de existir.

## 6. Transbordo em cadeia
- **Gatilhos:** `after_hours`, `no_eligible_agent`, `capacity_exhausted`, `max_wait` (corrido ou útil), `sla_risk`, `sla_breach`, `operational_failure` (N falhas seguidas de entrega/roteamento).
- **Etapa:** `{ posição, gatilhos[], espera (s, corrida|útil), destino, ação, mensagem opcional }`. Exemplo do dono: N1 (5 min) → N2 → plantão/Hub → contingência (mensagem informativa, pendência, nova tentativa ou intervenção humana).
- **Destinos tipados** com **chave canônica**: `agent:<uuid>`, `queue:<uuid>`, `sector:<uuid>`, `oncall:<roster uuid>`, `flow:<uuid>`, `hub:<contrato uuid>`. A conversa carrega `visited[]` com essas chaves; destino já visitado é recusado.
- **Sem laços (HIGH do Codex tratado):** o limite é `max_steps` **e** `visited`; o append à lista e a decisão são **atômicos** (mesma linha travada); um `EXECUTE_FLOW` que devolve a conversa ao motor chega com `Source=flow` e **herda** `visited/hops` (não zera); requeue automático **nunca** zera; só uma **ação humana** (atribuir/transferir/reabrir) abre novo `episode`. Fluxo de contingência não pode transferir para uma chave já visitada.
- **Destino indisponível:** a etapa é pulada e registrada (`rejected_reason`); a próxima tenta; esgotada a cadeia aplica `on_exhausted`: `keep_waiting` | `contingency` (mensagem + pendência + timer de nova tentativa) | `manual` (`REQUIRE_MANUAL_ACTION`, fila de supervisão). **Nenhuma conversa é perdida.**
- **Idempotência (HIGH do Codex tratado):** a chave única é `(tenant, conversa, episode, step, trigger)` **sem** `attempt`; o timer tem chave lógica única, `generation` para cancelar após mudança de agenda e máquina de estados com lease; `FOR UPDATE SKIP LOCKED` pega o timer, o **trabalho** serializa pela trava de `routing_engine_state` da conversa. Dois workers, evento duplicado ou retry produzem uma decisão.
- **Timers duráveis:** só no banco (`routing_engine_timers`); worker caído → lease expira → outro retoma; `attempts` limitado com recuo; nada em memória.

## 7. SLA
Métricas: primeira resposta, espera na fila, tempo até atribuição, resolução; alerta preventivo (`risk_percent`) e escalonamento (vira gatilho `sla_risk`/`sla_breach`). Contagem **corrida** ou **útil** (`AddBusiness` no calendário do **setor onde a conversa está no momento**). Precedência: fila > setor > tenant. **Após transferência: PRESERVADO** — o relógio continua do instante original; o prazo restante é recalculado no calendário do destino, e **nunca reinicia automaticamente** (um `on_transfer=recalculate` explícito só por política e fica no histórico).

## 8. Hub e autorização (HIGHs do Codex tratados)
- **Não existe transferência entre tenants.** "Hub como destino" significa **expor a conversa ao Hub** dentro do mesmo tenant: a conversa permanece da empresa; o destino `hub:<contrato>` só é válido se, na transação da decisão, existir **contrato ativo** Hub × empresa **cujo escopo de fila cobre** a fila de destino, **ao menos uma concessão viva que pode responder**, empresa não suspensa e o recurso permitido pelo `ceiling`. Nenhuma `membership` fictícia é criada; o acesso do operador é revalidado por `hub_serve` quando ele de fato abre a conversa (RLS é a segunda barreira).
- O **worker não reconstrói contexto delegado**: ele decide como sistema **dentro do tenant** com o `tenant_id` do estado armazenado; payload de evento nunca é autoridade; todos os ids de destino são relidos e checados contra o tenant na transação. Revogação/suspensão entre a decisão e o atendimento é coberta porque a decisão só **muda fila/estado**, não concede acesso.
- `routing_engine_state`, `routing_engine_timers`, `routing_engine_decisions`: RLS por tenant (membros leem com `agent.read`; escrita só pelo motor/sistema e por `agent.manage` na configuração); auditoria **sem conteúdo de mensagem**.

## 9. Flow Builder
Nós novos (finos, chamam o motor/calendário): **selecionar setor** (opções vêm da API do tenant; guarda `sector_id` + nome de exibição; setor renomeado funciona, desativado sai pela porta `unavailable`), **consultar horário**, **consultar disponibilidade**, **encaminhar para fila**, **atribuir agente**, **aguardar atendimento**, **executar política de transbordo**, **transferir**, **acionar contingência**. `business_hours` existente passa a delegar ao calendário compartilhado mantendo o mesmo JSON. Reexecução e evento duplicado: a chave do motor (`episode/step/trigger`) já os torna seguros.

## 10. Frontend (Fase 6)
Configuração: setores, filas por setor, calendários (semana, feriados, exceções, suspensão), escalas, plantões, estratégia de distribuição, SLA, **editor de cadeia** (sequência de destinos, condições e fallback). Monitoramento: filas em risco, esperas, conversas em `WAIT_UNTIL_OPEN`/`REQUIRE_MANUAL_ACTION`. Inbox: setor, fila, agente, tempo de espera, SLA, histórico de transferências. No Hub, sempre o contexto da instância; sem tenant vindo do cliente.

## 11. Fases e critérios de aceite (mapa para as 8 fases do *addendum*)

| Fase | Entrega | Estado | Aceite |
|---|---|---|---|
| 1 Auditoria | §1 | **feita** | tabela de reaproveitamento e lacunas |
| 2 Arquitetura/contratos | este ADR (+ OpenAPI nas fases com API) | **feita (proposta)** | revisão do Codex sem HIGH aberto |
| 3 Calendários e disponibilidade | domínio `scheduling` **IMPLEMENTADO**; **persistência + API + herança + suspensão + `GET /queues/{id}/availability` IMPLEMENTADOS** (migration 124, POSTGRES VERIFIED, branch); **escala individual (`agent_shifts`), plantão/contingência (`on_call_rosters`/`periods`, migration 125) e `eligibility.Evaluate` + `GET /queues/{id}/eligibility` IMPLEMENTADOS** (POSTGRES VERIFIED, branch); falta ligar a elegibilidade ao rodízio real (Fase 4) | **quase concluída** | testes temporais (DST, meia-noite, feriado, suspensão), isolamento A/B, escala/presença/capacidade independentes |
| 4 Motor | `Decide`, `routing_engine_state`, `least_loaded`, chamadas de `RouteNew`/fluxo | **IMPLEMENTADO atrás de flag** (`OMNIRA_ROUTING_ENGINE_ENABLED`, desligada; migration 127; POSTGRES VERIFIED na branch) — falta chamar o motor também de `RouteNew`/`assign_queue` do fluxo e a Fase 5 | concorrência (dupla atribuição), idempotência, compatibilidade com conversas existentes |
| 5 Transbordo e SLA | cadeias, timers, contingência, SLA | pendente | os 6 gatilhos, laços, destino indisponível, worker caído, agenda alterada com fila cheia |
| 6 Flow Builder e Inbox | nós + telas | pendente | fluxos antigos intactos; Vitest + E2E |
| 7 Segurança e testes | isolamento, Hub, concorrência, recuperação | pendente | matriz do pedido (financeiro fora do expediente, suporte 24x7 sem agentes, turno de madrugada, feriados, fila estourada, destino indisponível, loops, worker caído, agenda alterada, concorrência) |
| 8 Revisão independente | Codex por fase + riscos residuais | contínua | CRITICAL/HIGH tratados |

### 11.1 Evidência da Fase 3 (parcial), branch `feat/sector-scheduling-overflow`
Postgres real: herança fila > setor > tenant > nenhum, DST por fuso, feriado/exceção/suspensão, revisão e edição concorrente (`expected_revision` → 409), validação 400, limite de corpo 413, isolamento A/B (404 uniforme, FK composta recusa até o dono do banco), permissões (agente 403, supervisor gerencia, calendário padrão só administrador), backfill idempotente; 6 mutantes mortos; migrations 093–124 up/down/up. Codex sobre a fase: CRITICAL/HIGH 0; 7 MEDIUM/1 LOW tratados (corpo limitado, recusa honesta quando a RLS filtra, concorrência otimista, limite de suspensões sem mexer na revisão, 23505→409, nomes ≤80, `lock_timeout` na migration, aviso de perda de dados no `down`). **Fase 3 concluída na branch (2026-10-13):** escala individual, plantão/contingência, regra única de elegibilidade, OpenAPI de todas as rotas; gate de integração **38/38** (37 antigos + `internal/sectors/adapters`, de `git archive` da branch), migrations 093–125 up/down/up. Codex sobre `8cf3e15`: CRITICAL/HIGH 0; 4 MEDIUM + 1 LOW tratados (guarda no banco contra agendar quem não é membro, `calendar_only`/`wired_to_router`, consultas limitadas, savepoints, relógio único). **Atenção:** o instantâneo de elegibilidade **ainda não é a regra do roteador real** (`wired_to_router=false`) — a Fase 4 troca isso. As numerações 124/125 podem precisar de reajuste no merge se `main` ganhar migrations.

### 11.2 Padrão do dono (2026-10-13): todos disponíveis 24x7 e no Suporte
"Pode deixar todos os agentes por padrão disponíveis 24x7 e por padrão no suporte (fila padrão de transbordo); depois se ajusta cada caso de horário, plantão e setor por agente." Implementado na migration 126 e no código de criação: (a) **disponível 24x7** = nenhum calendário de escala por padrão (sem escala = sem restrição); o setor **Suporte** recebe o calendário `24x7 (padrão)`; (b) **todo atendente ativo** é membro disponível da fila Suporte (capacidade 5, o primeiro valor a ajustar por pessoa); (c) `tenants.default_overflow_queue_id` aponta para o Suporte e é o **destino de último recurso da cadeia** (Fase 5); (d) a função SQL única `seed_tenant_sector_defaults(tenant)` é usada pela migration (empresas existentes) e pela criação de empresa pelo Hub; um atendente **novo** habilitado pela tela entra no Suporte, quem já tem filas não é mexido; a função só **acrescenta** (nunca troca modo, nome, calendário próprio, capacidade ajustada nem a fila de transbordo que a empresa escolheu). Ajustes por caso continuam pelos mesmos recursos: escala (`PUT /shifts`), plantão (rosters), setor/filas (`PUT /queues/{id}/sector`, vínculos de agente).

### 11.3 Evidência da Fase 4 (parcial) e achados do Codex tratados
Motor em `internal/routing/engine`: `Decide` (ASSIGN_AGENT, ENQUEUE, WAIT_UNTIL_OPEN, REQUIRE_MANUAL_ACTION, NO_ACTION), timers duráveis (`routing_engine_timers`: lease, geração, **token de fencing**, tentativas, justiça por tenant), replanejamento na mesma transação de qualquer mudança de calendário (`ReplanWaiting`), `least_loaded`. As tabelas se chamam `routing_engine_*` porque `routing_decisions` já existia (roteamento de tópicos, migration 065). **Revisão do Codex (commit `c53822d`): 0 CRITICAL, 5 HIGH + 7 MEDIUM, todos tratados:** ordem de trava única conversa → estado → timer (deadlock), candidato reavaliado pela mesma regra sob trava (inclusive a linha de `queue_members`, a mesma trava do rodízio legado: motor e legado juntos nunca estouram a capacidade), token de fencing por claim, replanejamento paginado e que nunca sobrescreve uma atribuição, decisão de registro preservada, escrita nas tabelas só pelo motor ou por quem tem `agent.manage`, FK composta de fila, fila acima de 2000 candidatos vira `candidates_truncated` visível (percorrer em páginas fica como seguimento), `least_loaded` recusado pela API com o motor desligado, `down` com aviso operacional. **Provas** (Postgres real): paridade de ordem com o rodízio legado, menos carregado, capacidade esgotada, 20 conversas × 4 agentes × 8 workers com eventos duplicados (12 vagas, nada passa da capacidade), legado + motor disputando a última vaga, presença fora do ar, setor fechado + timer + queda de worker + agenda alterada, deadlock sob estresse (decisões + timers + replanejamento), token antigo, candidato obsoleto recusado sob trava; 14 mutantes mortos, 1 equivalente (o CAS do dono sob a trava da conversa).

### 11.4 Fase 5a — cadeia de transbordo (implementada na branch, atrás da mesma flag)
`internal/routing/overflow` (domínio puro: política, validação, `Next` com esperas, visitados e limite de saltos) + `overflow_policies`/`overflow_steps` (migration 128) + API `GET/PUT/DELETE /queues/{id}/overflow-policy` (valida destino no tenant, revisão otimista, auditoria) + o motor percorre a cadeia: **gatilhos** `after_hours`, `no_eligible_agent`, `capacity_exhausted`, `max_wait` (também em fila manual); **destinos** `queue` (checado ao vivo: existe, setor ativo, calendário aberto ou `allow_closed`) e `oncall` (menos carregado do roster, capacidade em toda a empresa, escala, presença); **sem laço**: `visited` por episódio, `hops <= max_steps`, origem protegida, episódio novo só por ação humana; **esperas** corridas ou em horário útil do calendário da origem, com timer durável `step_wait` por etapa (cancelados ao mudar de fila); **esgotamento** `keep_waiting`/`contingency` (ESCALATE → fila de supervisão)/`manual`. Cada transição fica em `routing_engine_decisions` (`<gatilho>:overflow`). **Ainda não:** destino `flow`, mensagem informativa de contingência, gatilhos de SLA e `operational_failure`.

### 11.5 Fase 5b — destinos `agent`, `sector` e `hub` (migration 129)
- **`agent`**: atendente ativo da empresa; capacidade = a maior que ele tem em qualquer fila (ou 5), contada na empresa toda; escala e presença como sempre; relido sob a trava por agente.
- **`sector`**: a primeira fila do setor (por nome) que não foi visitada, está aberta (ou `allow_closed`) e tem alguém elegível; sem ninguém, a primeira aceitável (a conversa espera lá). Setor inativo é recusado.
- **`hub`**: `destination_id` = **contrato**, `destination_queue_id` = fila da empresa de onde o Hub atende. **A conversa nunca sai da empresa**: ela muda para uma fila que o contrato cobre, e só se `hub_destination_ready` disser que há contrato vivo, fila dentro do escopo, empresa e Hub ativos e **ao menos um operador vivo que pode responder**. A função repete o predicado de `has_active_hub_access` e **uma prova-oráculo** (17 estados de contrato, concessão, Hub, empresa e usuário × fila dentro/fora do escopo) exige que as duas respondam igual. A empresa vê seus contratos por `tenant_hub_contracts` (com `agent.manage`), **nunca quem são os operadores**; nenhuma `membership` é criada; o acesso do operador continua sendo decidido por `hub_serve` na hora em que ele abre a conversa.
- **`flow`** fica para a Fase 6: precisa do consumidor do Flow Builder (um fluxo hoje nasce de um evento de mensagem recebida) e não será "decorado" aqui como se funcionasse.

### 11.6 Fase 5c — SLA (migration 130)
- **Políticas** (`sla_policies`) por tenant, setor ou fila; a mais específica vale, **métrica por métrica**: `first_response` (a primeira mensagem de uma **pessoa**; mensagem automática não conta), `queue_wait` (espera na fila **atual**), `assignment` (até alguém assumir) e `resolution` (até encerrar). Contagem `wall` (corrida) ou `business` (horário útil do calendário de onde a conversa está). `risk_percent` define o ponto de risco.
- **Relógios** (`conversation_sla`) criados pelo motor: `first_response` e `resolution` vivem **a conversa inteira**; `assignment` e `queue_wait` por episódio. **Transferir nunca reinicia**: o tempo já vivido fica (medido no calendário de onde estava) e só o que resta é replanejado no calendário de onde a conversa está agora; `queue_wait` é a exceção declarada (cada fila tem o seu relógio; o da fila que ficou para trás vira `cancelled`, mas continua no histórico).
- **Risco e violação** acontecem **uma vez** (estado do relógio + chave da decisão), com decisão `ESCALATE` (`sla_risk:<métrica>` / `sla_breach:<métrica>`), evento `job.routing.sla_at_risk.v1` / `job.routing.sla_breached.v1` (id da conversa e a métrica, nunca conteúdo) e timers duráveis (`sla_risk`, `sla_breach`). Conversa **sem dono**: a violação vira gatilho `sla_risk`/`sla_breach` da cadeia de transbordo. Conversa **com dono**: só avisa — **o SLA nunca reatribui sozinho**.
- API: `GET/PUT/DELETE /sla-policies`, `GET /queues/{id}/sla` (monitor: contagens e prazos mais próximos). Política nova vale para relógios novos (um prazo em curso não muda de lugar debaixo das pessoas).
- Entregues depois: mensagem informativa fora do expediente (§11.7) e destino `flow` (§11.8). **Decisão:** `operational_failure` NÃO é gatilho da cadeia — presença (Valkey) fora do ar é falha de infraestrutura e a conversa é reenfileirada com nova tentativa, nunca "transbordada" para outro lugar por causa de uma oscilação; timers com falhas repetidas terminam em ação manual (`manual_required`).

### 11.7 Fase 5d — aviso ao cliente fora do expediente (migration 132)
- `queues.closed_notice` (≤ 500 caracteres, texto simples; `{{proxima_abertura}}` vira "segunda-feira, 14/10 às 09:00" no fuso do calendário, e some se a abertura é desconhecida). **Opt-in por grupo**: vazio = o motor não diz nada (um fluxo publicado que já avisa continua avisando; o editor alerta para não duplicar).
- O motor envia **uma vez por episódio** (chave idempotente `routing-closed:<conversa>:<episódio>`) quando a conversa passa a esperar a abertura. Melhor esforço dentro de um *savepoint*: falha do provedor nunca altera, atrasa nem aborta a decisão (mutante morto). API `GET/PUT /queues/{id}/closed-notice` (`agent.read` / `agent.manage`, auditada).

### 11.8 Fase 6c — destino `flow` (migration 133)
- Um fluxo **publicado** da empresa, dos tipos `INBOUND` ou `AFTER_HOURS`, vira destino de etapa (validado ao salvar a política e de novo ao entrar na etapa). A etapa **inicia o fluxo na própria conversa**, que continua no grupo; o fluxo decide o que fazer depois (por exemplo `apply_overflow` ou `human_handoff`).
- Regras do dono (§12.5) cumpridas no banco e no código: **só inicia se a conversa não tem execução ativa** (`busy` → etapa pulada, a cadeia segue); a conversa tem de estar aberta, sem dono, e ser uma que a automação pode assumir (mesmo teste de um início por mensagem); o fluxo respeita o filtro de canal. Idempotente por chave (`routing-flow:<conversa>:<episódio>:<etapa>`).
- O fluxo roda **na mesma transação** da decisão; se ele próprio roteou a conversa (`assign_queue`, setor, hand-off, dono), o motor **para a cadeia** e deixa a fila gravada como estava: a decisão seguinte vê a diferença e abre **novo episódio** (como para qualquer outro que mova a conversa). `apply_overflow` que atribui uma pessoa atualiza o retrato do fluxo (`Facts.AssignedTo`) para os nós seguintes da mesma execução (nenhum consumidor novo, nenhum evento a perder), isolado por *savepoint*: erro do fluxo pula a etapa (`flow_error`), nunca derruba o roteamento. A etapa conta como salto no guarda de laços e o fluxo entra em `visited` (não reinicia no mesmo episódio). Sem o Flow Builder ligado no worker: `flows_unavailable`.
- Nós de fluxo (Fase 6a) e o destino `flow` são direções opostas e não se chamam em círculo: `apply_overflow` dentro de um fluxo ativo encontra `busy` e a cadeia segue.

### 11.9 Filas manuais passam pelo motor (achado da revisão da própria entrega)
- As filas-padrão (Comercial, Financeiro, Suporte; migration 123) são **manuais**. Antes, só filas `round_robin`/`least_loaded` recebiam o job `job.routing.assign.v1`; a fila manual nunca chegava ao motor e **aviso fora do expediente, relógios de SLA e transbordo não valeriam para elas**.
- Agora, **com o motor ligado** (`OMNIRA_ROUTING_ENGINE_ENABLED`), a conversa que entra numa fila manual (por `RouteNew` ou pelo nó `assign_queue`) também recebe o job. O motor **nunca atribui dentro de fila manual** (uma pessoa pega); ele aplica o horário (fora do expediente → aviso, gatilho `after_hours` da cadeia, timer de abertura), o SLA e `max_wait`. Fila manual **sem** `routing_retry_at`: a varredura de segurança continua só para filas automáticas (o job é durável e há timers).
- **Recuperação:** com o motor ligado, o job de uma conversa manual também tem `routing_retry_at`; a varredura de segurança reenfileira uma conversa manual **só enquanto o motor nunca a viu** (sem linha em `routing_engine_state`). Depois da primeira decisão ela tem estado e o motor cuida dela com timers: nenhuma releitura periódica de fila manual.
- Com o motor desligado nada muda (o parâmetro é falso). Exceção documentada: grupos que ficaram em `least_loaded` (só existem se o motor já esteve ligado) continuam recebendo job e são distribuídos por rodízio, em vez de ficarem parados. Conversas já abertas no momento de ligar só entram no motor quando algo as tocar; o guia de operação traz a consulta de retroalimentação.

### 11.10 Aviso ao abrir (migration 134) — pedido do dono após o teste real de 2026-10-14
- **Lacuna vista no WhatsApp real:** o cliente recebia "fechado, respondemos quarta às 09:35" e, quando o setor abria, **nada**: a conversa passava a "aguardando atendente" em silêncio e o cliente não sabia se seria atendido.
- `queues.open_notice` (≤ 500 caracteres, texto simples). **Opt-in** como o `closed_notice`: vazio = não diz nada. O motor envia **uma vez por episódio** (chave idempotente `routing-opened:<conversa>:<episódio>`) quando o timer durável `open_wakeup` acorda uma conversa que esperava a abertura **e** a decisão a libera (`ENQUEUE` ou atribuição feita pelo motor). Não envia: com o grupo ainda fechado, em rota comum numa fila aberta, nem se a conversa já tinha dono. Melhor esforço dentro de *savepoint*: falha do provedor nunca altera nem aborta a decisão.
- API `GET/PUT /queues/{id}/open-notice` (`agent.read` / `agent.manage`, auditada como `queue.open_notice_saved`), independente do aviso de fechado. Tela: **Atendimento → Setores → "Avisos de horário"** (um modal com os dois textos; só o campo alterado é enviado).
- Evidência: Postgres real (`TestOpenNoticeIsOptInOnlyOnWakeupReleaseOncePerEpisodeAndNeverBreaksTheDecision`, filas manual e `round_robin`; mutante "sem envio" morto) e `TestOpenNoticeAPIIsIndependentFromTheClosedNoticeAndFollowsTheSameRules`; Vitest 980. **Não provado** em WhatsApp real (depende de implantar e de um novo teste com setor fechado).

### 11.11 Capacidade 0 = sem limite (migration 137) — pedido do dono em 2026-10-14

"Na quantidade de atendimento ao mesmo tempo, permita selecionar 0 (sem limite de atendimento simultâneo na fila)."

- **Regra:** `queue_members.capacity` vai de 0 a 100. **0 = sem limite** de conversas ao mesmo tempo naquela fila. Antes ia de 1 a 100.
- **Onde vale:** em todos os pontos que olham a capacidade:
  - rodízio legado (`AssignRoundRobin`, candidatos e tentativa);
  - atribuição a outra pessoa;
  - regra única de elegibilidade (`eligibility.Evaluate`, que serve o motor e a tela de simulação);
  - domínio `SelectNext`;
  - destino `agent` (sem limite em qualquer fila = sem limite);
  - plantão: o limite de um período de plantão **não** rebaixa quem não tem limite como membro da fila.
- **Plano do convite (ADR-0046):** aceita 0 (`queue_capacity` omitido = 1).
- **O que não muda:** assumir a conversa para si continua sem olhar a capacidade, como antes.
- **Implantação:** a regra está no código do **worker** e da **api**. Os dois sobem junto com a migration e a tela. Um worker antigo trataria 0 como "nunca elegível".
- **Down:** transforma 0 em 100.
- **Provas:** Postgres real (rodízio, atribuição, destino `agent`, plantão, rotas, convite), unidade (elegibilidade, domínio) e ida e volta da migration 137.

## 12. Decisões que precisam do dono (padrão adotado até resposta)
1. **Feriados:** cadastro manual por tenant (padrão) ou importação de calendário nacional.
2. **Fuso padrão do tenant:** `America/Manaus`, editável por calendário.
3. **Limite de etapas/saltos:** 4 por cadeia (teto 8).
4. **Fechar o expediente com conversa já atribuída:** nada muda; só conversas sem dono seguem `after_hours`.
5. **Fluxo como destino:** só se a conversa não tiver execução ativa (regra do banco: uma ativa por conversa).
6. **Setores iniciais:** Comercial/Financeiro/Suporte ganham setor homônimo por migração de dados (§0); a fila Default fica sem setor.
7. **Deploy:** esta branch **não** será mesclada nem implantada sem a sua ordem; as fases com migration serão provadas em banco descartável.

## 13. Fora do escopo
Importação de calendários externos, troca de escala entre agentes, integração com ponto/folha, SLA contratual por cliente, transferência de propriedade entre tenants.
