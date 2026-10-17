# Progresso — Service Context & Operations (arquivo de retomada)

Branch: `feat/omnira-service-context-operations` · worktree `/data/cafegpt/tmp/wt-svcctx` (base `main` @ `26e4f01`). Sem push/merge/tag.
Migrations reservadas: **000150–000159**. ADR: **0049**. Docs: `docs/service-context/`.

Como rodar testes (banco descartável, nunca `omnira_dev`): scripts no scratchpad da sessão (`svc-mkdb.sh`, `svc-gotest.sh`, derivados de `tn-*.sh`); Go em `golang:1.25` via docker; Codex = revisor somente leitura (`codex exec`), sem `go` no ambiente dele.

| Fase | Estado | Commit | Observação |
|---|---|---|---|
| 0 Auditoria | feito | d9cdfd4 | Codex: 4 HIGH/6 MEDIUM tratados |
| 1 Sessões/protocolo | feito | 78cf5e8 | Codex: 4 HIGH/5 MEDIUM tratados |
| 2 Timeline/contexto | feito | 78cf5e8 | sem outbox (decisão); entrega = headless |
| 3 Ocorrências/tarefas | feito | 80209d1 | Gate 3: 5 cenários; Codex: 5 HIGH tratados |
| 4 ERP/eventos | feito (adaptador real e webhook = BLOCKED) | f01d13b | Codex: 5 HIGH tratados |
| 5 Workspace/Hub | feito | 7bf2869, f4859f4, 793c9c1 | Codex: 2 HIGH/3 MEDIUM tratados |
| 6 Fluxos/IA/métricas | feito | 3e022f4, 1d5a4d8, 735dc1f | Codex: 2 HIGH (1 tratado, 1 = R-AI-1 herdado) |
| 7 Docs finais | feito | (este commit) | status **READY_FOR_REVIEW** (ver 13) |
| 8 Decisões do dono 2026-10-15 | feito (ver 14) | (próximo commit) | prefixo por empresa + 1001, janela 24 h, abertura ao assumir |
| 9 Pós-merge (2026-10-15/17) | feito, implantado | `5f970b1` … `b79094f` | aviso do protocolo por empresa; paridade do painel do Hub; anexos no Hub; status do chamado ERP pelo Hub (chave por empresa); prefixo, protocolo na conversa, ocorrências/OS/histórico, resultado ao finalizar; R-7 fechado; e2e, restore e carga; contrato do adaptador de ERP; Codex sem BLOCKER/HIGH |
| 10 Noite 2026-10-17 | feito, implantado (`bb96ea2`, sem migração, sem push) | `8de7bf5`, `edde0ad`, `19fae68`, `bb96ea2` | enviar conversa a outra fila/setor (`route-queue`, só membros); "Cliente sem empresa" (`standalone:true`); P0/P1 do headless registrados e roteiro do P2 (`docs/ops/HEADLESS-P2-CHECKLIST.md`); Codex: 1 HIGH + 4 MEDIUM, os aplicáveis tratados |

Retomada: nada a retomar no código; pendências estão em `13-final-report.md`.
