# Motor de roteamento (ADR-0044) — estado local, para retomar sem perder o fio

Branch `feat/sector-scheduling-overflow` (base `c804344` de `main`), worktree `/data/cafegpt/tmp/wt-sched`. **Não mesclada, não enviada, não implantada.** Memória: `~/.claude/projects/.../memory/sector-scheduling-overflow-branch.md`. Decisão do dono (2026-10-13): seguir as fases sem perguntar até a entrega do PRD; pendências que dependam dele são listadas aqui (seção 4); **merge e implantação continuam dependendo da ordem dele** (instrução explícita anterior).

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

## 2. Falta (ordem de execução)
1. **Fase 6a — Flow Builder**: nós finos que chamam os serviços (consultar horário da fila/setor, consultar disponibilidade, executar transbordo/encaminhar pelo motor, acionar contingência), `select_sector` dinâmico; destino `flow` do transbordo; compatibilidade dos fluxos publicados.
2. **Fase 7 — segurança**: matriz A/B de todas as rotas novas, contexto delegado do Hub recusado, concorrência/recuperação (parte já feita nos testes do motor).
3. **Fase 6b — interface**: configuração (setores, calendários, escalas, plantões, cadeia, SLA) e monitoramento; Inbox mostrando setor, fila, agente, espera, SLA.
4. **Fase 8 — fechamento**: Codex sobre a branch inteira, guia de operação, matriz de aceite do PRD, relatório final.

## 3. Armadilhas (resumo; detalhe na memória)
Nunca `git checkout <arquivo>` para desfazer mutante com trabalho não commitado (copiar para `/data/cafegpt/tmp/*.o`); rodar **todas** as suítes afetadas antes de commitar (um teste de monitor quebrou por coluna nova); tabelas do motor são `routing_engine_*` (`routing_decisions` já existe); matrizes de permissão (`iam3/security_test.go`, `tenancy/adapters/team_http_test.go`) listam toda permissão de administrador; roster sem setor cobre TODAS as filas; violação de unicidade aborta a transação da requisição (pré-checagem ou `attempt()`).

## 4. Pendências que dependem do dono (acumuladas)
1. **Autorizar o merge e a implantação da branch** (migrations 124–131 renumeráveis se o `main` ganhar migrations; ligar `OMNIRA_ROUTING_ENGINE_ENABLED` só depois, por empresa/horário combinado).
2. As cinco decisões da seção 12 do ADR-0044 (feriados manual ou calendário nacional; fuso padrão; limite de etapas; conversa já atribuída ao fechar o expediente; fluxo como destino) — seguem os padrões adotados até resposta.
3. Nomes reais dos setores por empresa e de como "Servidores/Outsourcing TI/Monitoring" recebem trabalho (S1 do ADR-0043).
4. Capacidade padrão 5 dos atendentes no Suporte (palpite meu) — confirmar ou ajustar.
5. As pendências do Hub (Keycloak e-mail editável, ACLs de Valkey/NATS, testes com aparelho/ERP/IA reais, assinatura do gate, ADR-0041).
