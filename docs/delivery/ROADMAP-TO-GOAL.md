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
| P1 | M05.6 — UI usa sessão real (login backend, tenant, rotas, SSE autenticado, GET conversa) + realtime real + e2e em navegador | ✅ DONE | P0 |
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

### P1 — M05.6 sessão real + realtime real ✅
**Achados (defeitos herdados de M05.2/M05.3, todos corrigidos):**
1. Login do frontend era 100% mock (JWT com "mock-signature", rejeitado pelo backend). Agora `authAPI.login` chama `POST /api/v1/auth/login`; `tenantId` vem da resposta e é gravado (`lib/session.ts`). `authAPI.mockLogin` + `VITE_MOCK_AUTH=true` mantêm o modo offline.
2. Páginas usavam `http://localhost:8080` (CORS) e `tenantId`/`jwtToken` que ninguém gravava. Agora same-origin `/api/v1` (`lib/config.ts`; proxy do vite/nginx), token em `token`.
3. Rota `/inbox/:conversationId` recebia o id via `?id=`; Inbox não tinha navegação. Corrigido (`ConversationRoute`, itens clicáveis, botão voltar, link na Sidebar).
4. `ConversationPage` nunca carregava a conversa (só havia listagem). Novo `GET /api/v1/tenants/{tid}/inbox/conversations/{id}` (tenant-scoped, 404 sem oráculo).
5. **Nenhum produtor de evento realtime** existia (`PublishConversationEvent` nunca chamado). Agora triggers (migration `000026`) emitem `pg_notify('omnira_inbox_events')` em INSERT de conversa/mensagem, mudança de `assigned_to_user_id`/`status` da conversa e de `status` da mensagem (cobre inbound, outbound, ack do webhook, worker e atribuição). Ponte no worker (`internal/worker/realtime`, LISTEN → NATS `inbox.events.<tenant>.<conv>`, reconecta com backoff). **Decisão:** NOTIFY (efêmero, transacional, sem tabela) em vez do Outbox durável, para não poluir/atrasar jobs de routing/envio. Eventos só com **referências** (sem corpo/telefone); a UI faz refetch pela API autorizada.
6. SSE (M05.2) segurava uma **transação de DB aberta por conexão** (middleware) e a "reautorização por evento" só relia um contexto estático. Reescrito: autoriza 1× em transação curta (`StreamMiddleware`), **reautoriza a cada 30 s** (membership revogada/tenant inativo fecha o stream), heartbeat 20 s, remove `Access-Control-Allow-Origin: *`, `X-Accel-Buffering: no`, desabilita o `WriteTimeout` de 15 s por resposta e não usa mais escrita concorrente.
7. Hook SSE recriava a conexão a cada render, não reconectava e não enviava token. Agora `fetch`-streaming com `Authorization`, backoff, refs para callbacks e `onReconnect` (refetch de eventos perdidos). 401 → limpa sessão e vai para `/login`.
8. Listas: "Load more/older" substituía a página; agora mescla por id (status atualiza, ordem estável).

**Evidência:** Go completo verde (build/vet/test) com Postgres real como `omnira_app`, NATS real; migrations up/down/up e down-all→up-all; SSE: entrega, isolamento tenant/conversa, 401/404/400, fechamento por revogação, sobrevive a `WriteTimeout`; triggers (ordem, sem vazamento de conteúdo, rollback não notifica); ponte (entrega + recuperação após `pg_terminate_backend`); web vitest 38/38 + `tsc`; **e2e no navegador (Playwright/Chromium) 9/9** contra API+worker+NATS+Postgres reais (`scripts/e2e-inbox.sh`): login real, inbox→conversa, assumir/responder/soltar persistindo, erro acionável ao enviar sem assumir, **realtime sem reload** (inbound, status sent→delivered, nova conversa), conversa de outro tenant não vaza, token inválido → login. Mutação: sem worker o teste de realtime falha.

**Como rodar o e2e:** `scripts/e2e-inbox.sh` (precisa `omnira-postgres`, `omnira-nats`, `web/node_modules`, chromium do Playwright). `--keep` mantém a stack.

**Pendências P1 (para outro agente):**
- **Autenticação real (OIDC/IdP)**: o backend só tem o *mock login* (`internal/platform/authn/mock_login.go`: e-mails fixos, chaves RSA geradas a cada boot → tokens invalidam ao reiniciar; múltiplas réplicas não validam entre si). É bloqueio para qualquer produção.
- Eventos realtime são best-effort (perdidos se o worker/ponte cair; com N workers cada um publica → duplicatas, inofensivas pois a UI só invalida queries).
- A ponte ocupa 1 conexão do pool do worker permanentemente.
- Telas legadas (Dashboard/Contas/Tickets/Relatórios/Supervisor) seguem com dados **mock** (`web/src/lib/api.ts`); specs e2e antigos (`web/e2e/{auth,dashboard,accounts}.spec.ts`) assumem esse mock e o domínio de produção.
- Sem paginação infinita/scroll automático; sem indicador de digitação/leitura; `unread_count`/`message_count` do tipo `ConversationItem` não vêm da API.
