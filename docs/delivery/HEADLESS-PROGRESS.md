# Headless — progresso da branch `feat/headless-crm-dual-operation`

Arquivo de estado persistente: reabrir ao retomar após compactação. Base `main@88e1828`. Worktree: `/data/cafegpt/tmp/wt-headless`. Sem push/merge/deploy.
Escopo (decisão do dono, 2026-10-14): **H0–H6 direto**, gates automáticos. H7 só documentado.

## Regras de operação desta branch
- Nunca mexer no checkout compartilhado nem nas outras worktrees. Migrations só em banco descartável (`scripts/test-integration.sh`).
- Gate de integração: `bash scripts/test-integration.sh ./pkg ...` (só pacotes; sem args = todos, >10 min). Rodar de **`git archive`** se o `git config` de worktree atrapalhar dentro do contêiner.
- Revisão independente: agente `codex:codex-rescue` (somente leitura) ao fim de cada fase; CRITICAL/HIGH bloqueiam.
- Números de migration: 135 em diante (livres em todas as branches em 2026-10-14). Reconferir antes de cada migration.

## Estado por fase
| Fase | Estado | Commit | Evidência |
|---|---|---|---|
| H0 | em andamento | — | ADR-0046, HEADLESS-INVENTORY, HEADLESS-THREAT-MODEL |
| H1 | pendente | | |
| H2 | pendente | | |
| H3 | pendente | | |
| H4 | pendente | | |
| H5 | pendente | | |
| H6 | pendente | | |

## Decisões-chave (ver ADR-0046)
A (controle tipado em `conversations`) + gatilho funil BEFORE; `assigned_to_user_id` = projeção humana. Aplicação = instalação por tenant, token opaco com digest, `SECURITY DEFINER` para autenticar, execução em sessão de sistema + recheck `FOR SHARE`. Journal `domain_events` com cursor por tenant (ordem de commit), gatilhos AFTER. Recibos próprios de comando externo.

## Próxima ação
Revisão Codex de H0 → corrigir → commit H0 → iniciar H1.
