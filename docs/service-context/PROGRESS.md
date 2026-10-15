# Progresso — Service Context & Operations (arquivo de retomada)

Branch: `feat/omnira-service-context-operations` · worktree `/data/cafegpt/tmp/wt-svcctx` (base `main` @ `26e4f01`). Sem push/merge/tag.
Migrations reservadas: **000150–000159**. ADR: **0049**. Docs: `docs/service-context/`.

Como rodar testes (banco descartável, nunca `omnira_dev`): scripts no scratchpad da sessão (`svc-mkdb.sh`, `svc-gotest.sh`, derivados de `tn-*.sh`); Go em `golang:1.25` via docker; Codex = revisor somente leitura (`codex exec`), sem `go` no ambiente dele.

| Fase | Estado | Commit | Observação |
|---|---|---|---|
| 0 Auditoria | feito | d9cdfd4 | Codex: 4 HIGH/6 MEDIUM tratados |
| 1 Sessões/protocolo | feito | 78cf5e8 + endurecimento | Codex: 4 HIGH/5 MEDIUM tratados (recibos, máquina de estados no banco, ator, leitura por chave) |
| 2 Timeline/contexto | feito | 78cf5e8 + endurecimento | sem outbox (decisão); entrega = headless |
| 3 Ocorrências/tarefas | feito | 80209d1 + endurecimento | Gate 3: 5 cenários; Codex: 5 HIGH tratados (recibo de passo de fluxo, fatos de conclusão, motivo de reabertura no banco, responsável da empresa) |
| 4 ERP/headless/eventos | feito (adaptador real e entrega de webhook = BLOCKED, ver 06/08) | pendente | porta + fake, tentativa durável, reconciliação, atualização de entrada; envelope + AsyncAPI |
| 5 Workspace/Hub | não iniciada | | |
| 6 Fluxos/IA/métricas | não iniciada | | |
| 7 Docs finais | não iniciada | | |
