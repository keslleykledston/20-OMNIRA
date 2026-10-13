# Motor de roteamento (ADR-0044) — estado local, para retomar sem perder o fio

Branch `feat/sector-scheduling-overflow` (base `c804344` de `main`), worktree `/data/cafegpt/tmp/wt-sched`. **MESCLADA em `main` (`1f30046`), enviada e IMPLANTADA em 2026-10-13 por ordem do dono (flag ligada, só conversas novas); este arquivo é histórico de retomada. Ver `ROUTING-ENGINE-FINAL-REPORT.md` §7.** Memória: `~/.claude/projects/.../memory/sector-scheduling-overflow-branch.md`. Decisão do dono (2026-10-13): seguir as fases sem perguntar até a entrega do PRD; pendências que dependam dele são listadas aqui (seção 4); **merge e implantação continuam dependendo da ordem dele** (instrução explícita anterior).

## 1. Feito (commits na branch; gate de integração, migrations 093–131 up/down/up, Codex por fase)
| Fase | Entrega | Migration |
|---|---|---|
| A | `internal/scheduling` (calendário puro: DST, madrugada, feriados, exceções, suspensão, horário útil) | — |
| 3 | setores + calendários + herança + API + `availability` | 124 |
| 3 | escala do agente, plantão/contingência, regra única de elegibilidade, `eligibility` | 125 |
| 3 | padrão do dono: todos disponíveis 24x7 e no Suporte, fila padrão de transbordo | 126 |
| 4 | motor `Decide` (estado, decisões, timers duráveis, `least_loaded`) atrás de `OMNIRA_ROUTING_ENGINE_ENABLED` | 127 |
| 5a | cadeia de transbordo (fila, plantão), API da política, `routing.override` | 128 |
| 5b | destinos agente, setor, Hub (a conversa nunca sai da empresa) | 129 |
| 5c | SLA (políticas, relógios, risco/violação uma vez, transferência não reinicia) | 130 |
| fix | `tenant_id` imutável nas tabelas novas | 131 |

## 1b. Feito depois (commits na branch)
| Fase | Entrega |
|---|---|
| 6a | nós do Flow Builder `check_schedule`, `check_availability`, `select_sector`, `assign_sector`, `apply_overflow` (finos; chamam calendário/elegibilidade/motor; simulador com `SimRouting`; publicação valida setor e falha fechada; Codex: 1 HIGH + 3 MEDIUM tratados) |
| 5d | aviso ao cliente fora do expediente (`queues.closed_notice`, migration 132; uma vez por episódio; savepoint; API GET/PUT) |
| 7 | segurança: rotas de administração inalcançáveis agindo por Hub (404, mutante morto); chaves não delegáveis; leitura de contratos do Hub sem operadores |
| 6b | UI: Atendimento › Setores, Horários, Escalas e plantões, Transbordo e SLA; painel "Roteamento e SLA" na conversa (membro e Hub delegado); `GET /inbox/conversations/{id}/routing` |

## 2. ESTADO FINAL (2026-10-13): todas as fases codificadas e verificadas; ver `ROUTING-ENGINE-FINAL-REPORT.md`. O que segue em §2 abaixo está FEITO, salvo a Fase 8 que agora está concluída.

## 2b. (histórico) Falta (ordem de execução)
1. ~~Destino `flow`~~ FEITO (ADR §11.8, migration 133): `Engine.StartRouted` no Flow Builder, mesma transação, só sem execução ativa.
2. Chamar o motor também em `RouteNew`/`assign_queue` (hoje o motor entra pelo job `job.routing.assign.v1`, que já cobre os dois caminhos quando o grupo é round_robin/least_loaded).
3. ~~UI `least_loaded`~~ FEITO.
4. **Fase 8**: gate completo da branch, Codex sobre a branch inteira, guia de operação, matriz de aceite do PRD, relatório final.

## 3. Armadilhas (resumo; detalhe na memória)
Nunca `git checkout <arquivo>` para desfazer mutante com trabalho não commitado (copiar para `/data/cafegpt/tmp/*.o`); rodar **todas** as suítes afetadas antes de commitar (um teste de monitor quebrou por coluna nova); tabelas do motor são `routing_engine_*` (`routing_decisions` já existe); matrizes de permissão (`iam3/security_test.go`, `tenancy/adapters/team_http_test.go`) listam toda permissão de administrador; roster sem setor cobre TODAS as filas; violação de unicidade aborta a transação da requisição (pré-checagem ou `attempt()`).

## 3b. Achado pré-existente (não introduzido pelo ADR-0044)
O SQL do rodízio legado conta as conversas abertas do atendente com o snapshot da instrução e só depois trava a linha de `queue_members`
(`FOR UPDATE ... SKIP LOCKED`). Dois workers legados — ou um legado e um do motor — rodando NO MESMO INSTANTE sobre a última vaga de um atendente
podem passar os dois na contagem e estourar a capacidade em 1. O motor recontaja com instrução nova depois da trava (provado em
`TestConcurrentWorkersNeverExceedCapacityNorAssignTwice`). Regra operacional: ao ligar `OMNIRA_ROUTING_ENGINE_ENABLED`, reiniciar os workers
juntos; não manter workers legado e motor ao mesmo tempo numa fila. Corrigir o SQL legado fica fora do escopo (código em produção, byte a byte).

## 4. Pendências que dependem do dono (acumuladas)
1. **Autorizar o merge e a implantação da branch** (migrations 124–131 renumeráveis se o `main` ganhar migrations; ligar `OMNIRA_ROUTING_ENGINE_ENABLED` só depois, por empresa/horário combinado).
2. As cinco decisões da seção 12 do ADR-0044 (feriados manual ou calendário nacional; fuso padrão; limite de etapas; conversa já atribuída ao fechar o expediente; fluxo como destino) — seguem os padrões adotados até resposta.
3. Nomes reais dos setores por empresa e de como "Servidores/Outsourcing TI/Monitoring" recebem trabalho (S1 do ADR-0043).
4. Capacidade padrão 5 dos atendentes no Suporte (palpite meu) — confirmar ou ajustar.
5. As pendências do Hub (Keycloak e-mail editável, ACLs de Valkey/NATS, testes com aparelho/ERP/IA reais, assinatura do gate, ADR-0041).
