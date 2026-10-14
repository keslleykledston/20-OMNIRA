# Headless — progresso da branch `feat/headless-crm-dual-operation`

Arquivo de estado persistente: reabrir ao retomar após compactação. Base `main@88e1828`. Worktree: `/data/cafegpt/tmp/wt-headless`. Sem push/merge/deploy.
Escopo (decisão do dono, 2026-10-14): **H0–H6 direto**, gates automáticos. H7 só documentado.

## Regras de operação desta branch
- Nunca mexer no checkout compartilhado nem nas outras worktrees. Migrations só em banco descartável (`scripts/test-integration.sh`).
- Gate de integração: `bash scripts/test-integration.sh ./pkg ...` (só pacotes; sem args = todos, >10 min). Rodar de **`git archive`** se o `git config` de worktree atrapalhar dentro do contêiner.
- Revisão independente: agente `codex:codex-rescue` (somente leitura) ao fim de cada fase; CRITICAL/HIGH bloqueiam.
- Números de migration: 135 em diante (livres em todas as branches em 2026-10-14). Reconferir antes de cada migration.

## Estado por fase
| Fase | Estado | Commits | Evidência |
|---|---|---|---|
| H0 | concluída | 8202064 | ADR-0046, inventário, modelo de ameaça; Codex H0 (2 CRITICAL + 4 HIGH) incorporado ao desenho |
| H1 | concluída | d58e803, b729e49 | migration 135; gate + mutantes h1; Codex H1 corrigido |
| H2 | concluída | 7499f21, 78f4c8c | migration 136; controle tipado + escritores legados; mutantes h2 |
| H3 | concluída | 7499f21, 78f4c8c | API de leitura; mutantes h3; Codex H2/H3 corrigido |
| H4 | concluída | 78f4c8c, 1ea072d, 0d8dca8, a509d61 | migrations 137/138; journal, eventos, webhooks; mutantes h4; Codex H4 corrigido; defeito do gatilho legado achado pelo E2E |
| H5 | concluída | 943909a, 7af18b4 | migration 139; escrita coordenada; mutantes h5; Codex H5 corrigido (lease, TOCTOU, capacidade...) |
| H6 | concluída (exceto canal real) | f93ee4d, a509d61, 12d6ac1 | UI, simulador, E2E, contratos, runbook, benchmark 30k; **gate externo REAL-PROVIDER BLOCKED** |

## Pendências (nada disto foi feito sem ordem)
- Revisão final transversal do Codex: ver HEADLESS-FINAL-REPORT.md.
- Teste real por WhatsApp: roteiro em `docs/ops/HEADLESS-LIVE-TEST.md` (número de teste autorizado pelo dono); precisa de quem escaneie o QR de um remetente de teste.
- Push / merge / deploy: **não ordenados**.
- H7 (identidade humana verificada do CRM, federação): fora do primeiro release, só documentado.

## Decisões-chave (ver ADR-0046)
A (controle tipado em `conversations`) + gatilho funil BEFORE; `assigned_to_user_id` = projeção humana. Aplicação = instalação por tenant, token opaco com digest, `SECURITY DEFINER` para autenticar, execução em sessão de sistema + recheck `FOR SHARE`. Journal `domain_events` com cursor por tenant (ordem de commit), gatilhos AFTER. Recibos próprios de comando externo.

## Próxima ação
Aguardar a revisão final do Codex; corrigir CRITICAL/HIGH; entregar a resposta final. Depois, o dono decide push/merge e o teste real.
