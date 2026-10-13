# ADR-0044 — matriz de aceite do PRD (horários, plantões, transbordo, SLA)

Vocabulário (o mesmo do Hub): **IMPLEMENTED** (existe), **UNIT**, **POSTGRES** (Postgres real, runtime sob RLS), **HTTP** (cadeia real de middleware), **UI-TEST** (vitest/RTL), **E2E** (navegador + aparelho/serviços reais), **NOT RUN**, **BLOCKED**. Nada aqui está implantado: flag `OMNIRA_ROUTING_ENGINE_ENABLED` desligada, branch `feat/sector-scheduling-overflow` sem merge. **Nenhuma linha é E2E**: não houve navegador real, aparelho WhatsApp, ERP ou IA reais para este módulo.

## 1. Requisitos do complemento obrigatório
| # | Requisito | Estado | Evidência |
|---|---|---|---|
| 1 | Setores com grupos e atendentes; setor Financeiro/Comercial/Suporte | IMPLEMENTED · POSTGRES · UI-TEST | migrations 123/124; `sectors/adapters` (handler tests); `AttendancePages.test.tsx` |
| 2 | Horário por setor: 24x7 / comercial / personalizado; múltiplos intervalos; turno que cruza a meia-noite; exceções, feriados, suspensão; fuso IANA e horário de verão | UNIT · POSTGRES · UI-TEST | `internal/scheduling` (calendário puro, DST, madrugada); `TestSectorsCalendarsAndInheritedAvailability`, `TestCalendarEditsHolidaysSuspensionsAndRevision`; `zonedToIso` (fuso do horário, não do navegador) |
| 3 | Separar horário do setor × escala do atendente × presença × capacidade (motivos listados) | UNIT · POSTGRES | `internal/routing/eligibility`; `eligibility_integration_test.go`; tela “Situação agora” |
| 4 | Plantão e contingência (períodos, capacidade durante o plantão) | POSTGRES · UI-TEST | `oncall.go` + testes; `pickOnCall` (re-checado sob trava) |
| 5 | Transbordo: fora do expediente, sem atendente elegível, capacidade esgotada, tempo máximo, SLA em risco/violado | POSTGRES | `chain_integration_test.go`, `sla_integration_test.go`. **Falha operacional não é gatilho** (decisão registrada no ADR §11.6) |
| 6 | Destinos: atendente, fila, setor, plantão, Hub, **fluxo** | POSTGRES · UNIT | `destinations_integration_test.go`; `flow_destination_integration_test.go`; `StartRouted` (flows) |
| 7 | Validação prévia ao salvar a política (inclusive Hub): destino pertence à empresa, está ativo, contrato válido | HTTP/POSTGRES | `TestOverflowPolicyNewDestinationKindsAreValidatedAgainstTheCompany`; `hub_destination_ready` com prova-oráculo |
| 8 | Sem ciclos/duplicidade; limite de etapas | UNIT · POSTGRES | `overflow_test.go` (Validate/Next/visited); `max_steps`; UI avisa antes de enviar |
| 9 | Histórico preservado e auditoria | POSTGRES | `routing_engine_decisions` (uma linha por episódio/etapa/gatilho), audit events por ação; painel da conversa |
| 10 | SLA corrido ou útil; preservado na transferência; nunca reinicia para esconder atraso; risco/violação uma vez | POSTGRES | `sla_integration_test.go` (+ achados do Codex tratados) |
| 11 | Nós do Flow Builder sem duplicar regra (consultar horário/disponibilidade, escolher e enviar a setor, aplicar transbordo) | UNIT · POSTGRES | `routing_nodes_test.go` (simulador), `routing_effects_test.go`; Codex 6a: 1 HIGH + 3 MEDIUM tratados |
| 12 | Interface de configuração e monitoramento | UI-TEST | 4 páginas + painel na conversa; **sem E2E em navegador** |
| 13 | Motor central com decisões ASSIGN_AGENT/ENQUEUE/OVERFLOW/WAIT_UNTIL_OPEN/EXECUTE_FLOW/ESCALATE/REQUIRE_MANUAL_ACTION | POSTGRES | `engine_integration_test.go` |
| 14 | Mensagem informativa ao cliente fora do expediente | POSTGRES | `TestClosedNoticeIsOptInOncePerEpisode…`; envio é melhor esforço |

## 2. Simulações pedidas
| Cenário | Estado | Teste |
|---|---|---|
| Financeiro fora do expediente | POSTGRES | `TestClosedSectorWaitsForOpeningWithADurableTimer…`, `TestManualQueuesFollowTheirScheduleNoticeChainAndOpening` |
| Suporte 24x7 sem atendentes | POSTGRES | cadeia com `no_eligible_agent` → N2/plantão/contingência (`TestTheOwnersChainN1ThenN2…`) |
| Turnos que cruzam a meia-noite | UNIT | `internal/scheduling/calendar_test.go` |
| Feriados e exceções | UNIT · POSTGRES | idem + `TestCalendarEditsHolidaysSuspensionsAndRevision` |
| Fila estourada | POSTGRES | `TestCapacityExhaustedEnqueues…`; `TestConcurrentWorkersNeverExceedCapacityNorAssignTwice` |
| Destino indisponível | POSTGRES | `rejected` no histórico (`destination_closed`, `hub_not_ready`, `flow_busy`…) |
| Laços | UNIT · POSTGRES | `visited`, `max_steps`; um fluxo não move conversa já movida pela cadeia |
| Falha de worker / timers | POSTGRES | lease, fencing token, equidade entre empresas, desistência após 8 tentativas |
| Agenda alterada com conversas esperando | POSTGRES | `ReplanWaiting` na mesma transação da mudança |
| Concorrência e isolamento A/B | POSTGRES · HTTP | `…ConcurrentWorkers…`, matrizes A/B, `tenant_id` imutável (migration 131), Hub não administra (404) |

## 3. Fases do addendum
| Fase | Estado |
|---|---|
| 1 Diagnóstico | feito (ADR-0044 §0–§10) |
| 2 Modelo e migrations 124–133 | IMPLEMENTED, up/down/up verificado (`scripts/test-hub-migrations.sh`) |
| 3 Calendário, escala, plantão, elegibilidade | POSTGRES |
| 4 Motor, estado, timers, `least_loaded` | POSTGRES (atrás da flag) |
| 5 Cadeia, destinos, SLA, aviso, fluxo | POSTGRES |
| 6 Flow Builder + interface | UNIT/UI-TEST; sem E2E |
| 7 Segurança | HTTP/POSTGRES (isolamento, Hub, imutabilidade de tenant, conteúdo nunca em fila/eventos) |
| 8 Fechamento | Codex por fase + revisão final da branch + reconferências (sem achados abertos); gate 39/39 em `d41bdff`; guia de operação; relatório final |

## 4. O que NÃO foi provado (não declarar pronto)
- Sem E2E em navegador das telas novas e sem teste com aparelho/WhatsApp real (aviso fora do expediente, janela de 24 h da Meta).
- Sem carga: o motor decide uma conversa por transação; não foi medido com milhares de conversas esperando.
- A varredura de segurança do rodízio legado continua só para filas automáticas; filas manuais dependem do job durável + timers.
- Achado pré-existente (fora do escopo): corrida do SQL legado na última vaga (ADR/ESTADO §3b).
