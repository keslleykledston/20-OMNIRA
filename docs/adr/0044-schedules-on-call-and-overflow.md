# ADR-0044 — Agendas, plantões e transbordo (política temporal dos setores)

Status: **PROPOSTA com Fase A em implementação** (branch `feat/sector-scheduling-overflow`, base `c804344` de `main`, **sem merge e sem implantação** até autorização do dono). Estado do projeto: `LAB`.
Pedido do dono (2026-10-13): módulo de horários, plantões e transbordo para o projeto de Setores/Filas/Roteamento. Relaciona: ADR-0043 (a fila é o setor), ADR-0019 (Flow Builder), ADR-0038/0040 (Hub), ADR-0042 (contexto de sessão).
Vocabulário de evidência: IMPLEMENTED / UNIT / POSTGRES / HTTP / E2E VERIFIED / NOT WIRED / BLOCKED.

## 1. Diagnóstico (auditoria de 2026-10-13, no código de `main`)

| Necessidade | O que já existe | Lacuna |
|---|---|---|
| Agenda do setor | **Só** o nó `business_hours` do Flow Builder com agenda **embutida** no nó (`internal/flows/domain/hours.go`): fuso IANA, janelas por dia da semana, `[início, fim)` no mesmo dia. O próprio código diz "a plataforma não tem módulo de horário para reaproveitar". | Sem turno que atravessa a meia-noite, sem exceções/feriados, sem agenda por setor reutilizável fora do fluxo, sem 24x7 explícito, sem fuso do tenant (não há coluna de fuso em `tenants`; `docs/ops/TIMEZONE.md` é política de servidor, não do produto). |
| Escala do agente | `queue_members.available` (elegibilidade manual), `capacity`, `agent_profiles.status`. | Não há escala (turnos) do agente nem plantão/contingência. |
| Presença | Valkey (`internal/presence`), `tenants.routing_require_presence`, rodízio com presença (`assignRoundRobinPresenceAware`). | Presença está correta como está; só precisa ser **combinada** com agenda e escala, nunca confundida com elas. |
| Capacidade | `queue_members.capacity` e contagem de conversas abertas atribuídas na própria consulta de rodízio. | Capacidade esgotada não gera ação alguma: a conversa espera (varredura de 60 s refaz a tentativa). |
| Distribuição | `AssignRoundRobin` atômico (`FOR UPDATE SKIP LOCKED`), `ClaimUnassigned`, `assignment_events`, varredura de vida (`internal/worker/routing/liveness.go`, `routing_retry_at`, 200 por lote, atraso 90 s), pools do Hub (`hub/distribution`, `hubdistributor`). | Só rodízio e manual; sem "menor carga"; sem destino alternativo. |
| Timers | `flow_runs.wait_until` + `OnTimeout` (porta de timeout do fluxo); `routing_retry_at` da varredura. | Sem timer de **tempo de espera na fila** nem de SLA; sem retomada documentada após queda do worker para essa finalidade. |
| SLA | `tenants.wait_warn_minutes/wait_danger_minutes` — **só cor de tela** ("não é SLA contratual"). | Nenhum prazo, nenhuma violação, nenhum calendário útil. |
| Transferência/auditoria | `assignment_events` (de→para, quem, motivo, origem), `audit_events`, transferência manual (`ConversationAssigner`, `DelegatedTransfer` no Hub), `assign_queue`/`human_handoff` do fluxo (`flows/adapters/effects_postgres.go`, espelha o `RouteNew`). | Sem decisão de transbordo registrada, sem proteção contra laço, sem idempotência de "ação por motivo". |
| Hub | Transferência delegada só entre quem pode responder a instância (`replying.Service`), contratos e concessões. | Transbordo para destino de **outra** empresa não existe e **não será criado** sem contrato/autorização explícitos (§7). |

**Reaproveitar:** `AssignRoundRobin`/`ClaimUnassigned` (extraídos em serviço de distribuição), `assignment_events`, `routing_retry_at` + varredura, `outbox_events` + NATS, `audit_events`, `flow_runs.wait_until`, `queue_members`, presença do Valkey, `lock_served_tenant`/`has_active_hub_access`.
**Não duplicar no Flow Builder:** os nós novos só chamam os serviços de domínio abaixo; o nó `business_hours` passa a delegar ao calendário compartilhado (compatível: o mesmo JSON de configuração continua válido).

## 2. Arquitetura (serviços de domínio compartilhados)

```
internal/scheduling/        (domínio puro, sem I/O)         <- Fase A
  calendar.go   Resolver de expediente: fuso IANA, intervalos por dia, turnos que cruzam a meia-noite,
                exceções por data, feriados, "está aberto agora?", "próxima abertura/fechamento",
                "prazo = agora + N minutos úteis"
internal/scheduling/adapters (Postgres)                      <- Fases B–C
  agendas dos setores, escalas dos agentes, plantões
internal/routing/...        elegibilidade + distribuição     <- Fase C
  Eligibility(queue, now) = agenda aberta ∧ membro ativo ∧ disponível ∧ escala ∧ presença ∧ capacidade
internal/overflow/          política + motor de transbordo   <- Fase D
internal/sla/               prazo, risco e violação          <- Fase E
internal/flows/...          nós check_schedule e apply_overflow (finos) <- Fase F
web/                        configuração e monitoramento     <- Fase G
```

### 2.1 Quatro coisas separadas (requisito 4)
1. **Agenda do setor** (`sector_schedules`): quando o setor atende. Pertence à fila (setor), um registro por fila (ou nenhum = comportamento atual, "sempre aberto").
2. **Escala do agente** (`agent_shifts`): quando a **pessoa** trabalha naquele setor. Opcional; sem escala = "disponível quando marcada disponível" (hoje).
3. **Presença**: se a pessoa está conectada agora (Valkey). Inalterada.
4. **Capacidade**: quantas conversas a pessoa comporta (`queue_members.capacity`). Inalterada.
A elegibilidade é a **conjunção** das quatro, calculada num único lugar.

### 2.2 Tempo (requisitos 2 e 3)
- Instantes são `timestamptz` (UTC no armazenamento). Regras **locais** (dia da semana + minuto do dia) são guardadas como dados + **nome IANA** (`America/Manaus`), nunca como deslocamento fixo.
- O resolvedor transforma a regra em instantes por data local: `time.Date(ano, mês, dia, h, m, 0, 0, loc)`.
- **Horário de verão:** hora local inexistente (salto) é empurrada para frente pelo mesmo tempo (comportamento do Go); hora repetida (volta) vale a **primeira** ocorrência. Um expediente 09:00–18:00 tem a duração real do dia (8 h ou 10 h nos dias de mudança) — sempre medida em instantes, nunca somando 24 h.
- **Turno que cruza a meia-noite:** intervalo `22:00–06:00` pertence ao **dia em que começa** (a sexta noturna termina no sábado de manhã); `00:00–24:00` e a agenda 24x7 são tipos explícitos.
- **Exceções e feriados:** por data local (ou intervalo de datas) com `fechado` ou `intervalos próprios`; exceção vence feriado, feriado vence a regra semanal. Feriados por calendário do tenant (cadastro manual; sem importar listas externas nesta ADR).
- **SLA:** `wall_clock` (tempo corrido) ou `business` (conta só dentro da agenda do setor, com exceções e feriados).

### 2.3 Transbordo (requisitos 6–12)
- **Gatilhos** (`overflow_policies.rules[].trigger`): `after_hours`, `no_eligible_agent`, `capacity_exhausted`, `max_wait` (minutos corridos ou úteis), `sla_risk` (percentual do prazo) / `sla_breach`, `operational_failure` (falha de entrega/roteamento repetida).
- **Destinos:** `agent`, `queue`, `sector` (= fila de outro setor da **mesma** empresa), `on_call` (plantão), `flow` (fluxo automatizado). Cada regra tem **ordem** e **limite de tentativas**; a primeira regra cujo destino passa na validação vence; se nenhuma passa, a conversa permanece na fila e a falha é registrada (nunca é perdida).
- **Validação prévia do destino** (requisito 8): a mesma função de elegibilidade e de autorização usada pela transferência manual, **no momento da decisão e dentro da transação**: destino ativo, setor aberto (ou política de espera explícita), membro elegível, `can_reply`, e — no Hub — concessão viva/contrato/ceiling. Destino indisponível = regra pulada e registrada.
- **Sem laço e sem repetição** (requisito 9): cada conversa carrega `routing_path` (setores já visitados + contador de saltos, limite configurável, padrão 3); um destino já visitado é recusado. Cada decisão tem chave de idempotência `(tenant, conversa, episódio na fila, regra, tentativa)` com `UNIQUE`; repetir o evento/timer devolve a decisão já tomada. Reatribuição só ocorre com `UPDATE … WHERE assigned_to_user_id IS NOT DISTINCT FROM <esperado>` (compare-and-set) na mesma transação do registro.
- **Timers duráveis** (requisito: retry/concorrência): linha `overflow_timers(due_at, state, lease_until, attempts)` varrida por worker com `FOR UPDATE SKIP LOCKED`, lease curto e `attempts` limitado; queda do worker = lease expira e outro worker retoma; nada vive só em memória. Mesmo desenho da varredura de roteamento já em produção.
- **Histórico e auditoria** (requisito 10): a conversa **não muda de identidade** (mesmo `conversation_id`, mensagens intactas); cada decisão grava `overflow_decisions` (gatilho, regra, destino pedido/resolvido, resultado, motivo da recusa) e `assignment_events` quando muda o dono, mais `audit_events` sem conteúdo de mensagem.
- **Início/fim do expediente** (requisito 12), por política da fila: ao **fechar**, conversas em espera seguem `after_hours` (aviso/fluxo/transbordo/permanecer); ao **abrir**, a varredura reenfileira o roteamento das que aguardam (já existe o gancho `routing_retry_at`), com limite por lote.
- **Mudança de agenda com conversas aguardando:** salvar a agenda recalcula os timers da fila na mesma transação (os `due_at` derivam da agenda); nada espera por um horário que deixou de existir.

### 2.4 Entre empresas (requisito 8, última frase)
Nenhum destino pode estar em outra empresa. O Hub só transborda **entre instâncias que a mesma concessão/contrato já permitem atender**, e a regra do destino é revalidada como `hub_serve` (ADR-0040) no momento da decisão. Sem contrato explícito para isso, o destino é inválido por construção (a FK composta `(tenant_id, destino)` impede referência cruzada; o validador recusa o resto).

## 3. Fases incrementais e critérios de aceite

| Fase | Entrega | Aceite |
|---|---|---|
| **A — Calendário (domínio puro)** | `internal/scheduling`: agenda 24x7/comercial/personalizada, vários intervalos/dia, turnos noturnos, exceções e feriados, fuso IANA, horário de verão, `IsOpen`, `NextOpen`, `NextClose`, prazo útil | testes de unidade: meia-noite, DST (Nova York e um fuso sem DST como Manaus), feriado, exceção, 24x7, validação; fronteiras inclusive/exclusiva |
| B — Agenda persistida + API + nó `business_hours` delegando | migration (agenda por fila, exceções, feriados), CRUD com `agent.manage`, auditoria, contrato OpenAPI | Postgres real: isolamento A/B, RLS, 404 uniforme, compatibilidade do JSON antigo do nó |
| C — Escala, plantão, elegibilidade única | escalas, rosters de plantão/contingência, `Eligibility()` combinando agenda ∧ escala ∧ presença ∧ capacidade; "menor carga" como estratégia | concorrência (dupla atribuição), Hub, fora de escala |
| D — Motor de transbordo | políticas, decisões idempotentes, timers duráveis, anti-laço, validação de destino | gatilhos 6/6, loops, destino indisponível, worker caído, concorrência de dois workers, agenda alterada |
| E — SLA | prazos corrido/útil, risco e violação como gatilhos | calendário útil atravessando fim de semana/feriado |
| F — Flow Builder | nós `check_schedule` e `apply_overflow` (finos) | reexecução e evento duplicado seguros; sem lógica duplicada |
| G — Interface | configuração e monitoramento (agendas, políticas, filas em risco) respeitando o contexto do Hub | Vitest + E2E |
| H — Revisão independente | Codex por fase; documentação de operação | CRITICAL/HIGH tratados |

## 4. Decisões que precisam do dono (com padrão adotado até resposta)
1. **Feriados:** cadastro manual por tenant (padrão) ou importação de calendário nacional? *Padrão: manual.*
2. **Fuso padrão do tenant** para agendas sem fuso explícito: *padrão: America/Manaus (já é a política do servidor), editável por agenda.*
3. **Limite de saltos** de transbordo por conversa: *padrão 3.*
4. **Comportamento ao fechar o expediente com conversa já atribuída a um agente:** *padrão: nada muda (quem atende termina); só conversas **sem dono** seguem a política `after_hours`.*
5. **Transbordo por fluxo automatizado:** o fluxo recebe a conversa como nova execução (uma execução ativa por conversa já é regra do banco); *padrão: permitido apenas se não houver execução ativa.*

## 5. Fora do escopo desta ADR
Importação de calendários externos, escalas com troca entre agentes, integração com folha/ponto, SLA contratual por cliente, qualquer merge ou implantação sem autorização.
