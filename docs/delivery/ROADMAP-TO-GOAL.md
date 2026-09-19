# Roadmap até o GOAL — Inbox WhatsApp não oficial (WAHA) operável

> Documento vivo para qualquer agente que assuma o trabalho. Atualize a tabela e as seções
> "Evidência" e "Pendências" a cada fase. Regras do projeto: `CLAUDE.md`, `docs/architecture/*`.
> Nunca declarar produção pronta; estados permitidos em `~/.claude/CLAUDE.md` (K3G).

## GOAL (objetivo final de entrega)
Um operador humano consegue, pela UI e com login real do backend: **conectar um número WhatsApp (QR/WAHA) →
receber mensagens no Inbox → assumir a conversa → responder → ver o status (sent/delivered/read)**,
com isolamento multi-tenant (RLS como segunda barreira), rodando via `docker compose`, com testes,
runbook e pendências documentadas. Meta oficial (Meta Cloud) permanece preservada e **fora do escopo operacional**.

## Fases (ordem lógica)
| # | Fase | Estado | Depende de |
|---|------|--------|-----------|
| P0 | Fundação já entregue (H0/H1, M05.1–5.4, W1, W2, D3.1–3.3) | ✅ DONE | — |
| P1 | M05.6 — UI usa sessão real (login backend, tenant, rotas, SSE autenticado, GET conversa) + e2e em navegador | 🔄 EM ANDAMENTO | P0 |
| P2 | W-UI — Tela de conexões WAHA (criar c/ aceite de risco, QR com polling, status, start/stop) | ⏳ | P1 |
| P3 | Vertical E2E automatizado sem telefone (WAHA stub: webhook assinado → Inbox → reply → worker → ack) | ⏳ | P1 |
| P4 | Compose completo (migrations, web, worker) + runbook de operação | ⏳ | P1–P2 |
| P5 | Contrato OpenAPI das rotas M05/W + validação | ⏳ | P1–P2 |
| P6 | Gates de release (backup/restore, health/metrics, checklist LIMITED_INTERNAL_PRODUCTION_CANDIDATE) | ⏳ | P3–P5 |
| P7 | W3 — Smoke com telefone real (`scripts/w3-smoke.sh`) | ⛔ BLOQUEADO: requer telefone humano | P4 |

## Como verificar (comandos)
- Go (host não tem Go): `docker run --rm --network host -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e OMNIRA_DATABASE_URL=<owner> -e OMNIRA_APP_DATABASE_URL=postgres://omnira_app:omnira_app@127.0.0.1:55434/<db>?sslmode=disable golang:1.25 go test -count=1 -p 1 ./...`
- DB de teste: criar DB descartável em `omnira-postgres` (porta 55434), aplicar `migrations/*.up.sql` como owner.
- Web: `cd web && npx vitest run && npx tsc --noEmit`.
- Nunca `gofmt -w` na árvore toda (toca arquivos alheios).

## Registro por fase
(preenchido conforme as fases avançam)
