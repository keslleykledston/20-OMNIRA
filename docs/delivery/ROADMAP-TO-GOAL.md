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
| P2 | W-UI — Tela de conexões WAHA (criar c/ aceite de risco, QR com polling, status, start/stop) | ✅ DONE | P1 |
| P3 | Vertical E2E automatizado sem telefone (WAHA stub: webhook assinado → Inbox → reply → worker → ack) | ✅ DONE | P1 |
| P4 | Compose completo (migrations, web, worker) + runbook de operação | ✅ DONE | P1–P2 |
| P5 | Contrato OpenAPI das rotas M05/W + validação | ✅ DONE | P1–P2 |
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

### P2 — Tela de conexões WAHA ✅
`/channels` ("Canais" na Sidebar): lista conexões; **criar exige marcar o aceite de risco** (texto de banimento; registrado no backend com usuário e hora); iniciar sessão → polling de `GET .../{id}` a cada 2 s **somente após Start** → QR (`GET .../qr`, renovado a cada 8 s enquanto `needs_qr`) → "Connected as +número" quando ativa; parar sessão; 403 mostra "Only tenant administrators…" e esconde o formulário; 502/503/409/422 mapeados para mensagens acionáveis (`lib/channels.ts`).
**Evidência:** vitest 44/44 (`ChannelsPage.test.tsx`: criar c/ aceite, 403, QR→conectado, erros, stop) e e2e no navegador **contra um WAHA real** (`web/e2e/channels.spec.ts`, via `scripts/e2e-inbox.sh`, 11/11 no total): não-admin vê permissão; admin cria → `pending` + auditoria + `risk_acknowledged_by`; Start → **QR real decodificável** na tela; a chave HMAC não aparece no HTML; Stop → `disconnected` + 2 audits.
**Pendências P2:** pareamento com telefone real (P7); sem UI para remover/revogar conexão (só parar); sem exibir o número/nome do perfil além de `external_account_id`; lista não atualiza em tempo real (só ao criar/iniciar/parar).

### P3 — Vertical automatizado sem telefone ✅ (achou 2 bugs críticos de produção)
`internal/e2e/vertical_test.go` (Postgres real como `omnira_app`, handlers e middleware reais, credenciais cifradas, NOTIFY real; só o WhatsApp é um WAHA fake HTTP): admin cria conexão (aceite de risco) e ela vira `active` → webhook **assinado HMAC-SHA512** (assinatura errada = 401, sem dados) → conversa+contato+mensagem no Inbox; **redelivery do WAHA não duplica** → agente vê a conversa → responder antes de assumir = 409 → **corrida de 2 agentes: 1 vence (200), 1 perde (409)** → não-assignee = 403 → resposta idempotente (replay 200) → worker envia **uma única vez** ao WAHA com sessão/chatId/texto corretos (redelivery não reenvia) → acks assinados DEVICE→READ, duplicata ignorada e **ack antigo (PENDING) não rebaixa** → rejeição 422 do provedor = `failed`/`rejected` → tenant B não vê nada, 404 sem oráculo, credencial de outro tenant = 401. Também valida os eventos realtime (received/updated/status) por NOTIFY. Roda 3× com `-race`.
**Bugs de produção encontrados e corrigidos:**
1. **Webhook WAHA real nunca autenticaria** (401 mesmo com assinatura válida): `VerifyWebhook` lia a chave HMAC cifrada fora de sessão de tenant e o RLS de `channel_credentials` a escondia. Agora o handler roda a verificação em sessão de sistema do tenant da conexão (`UseSession`, ligado no `main` da API); falha de leitura da chave responde **503** (WAHA reenvia) e só assinatura inválida é 401. Verificado também na API real (e2e do navegador).
2. **RLS quebrava em conexão reutilizada** (`000027`): depois de uma sessão com `set_config(..., true)` o GUC volta como `''` e `is_system_admin()`/`current_user_id()` faziam `''::boolean/uuid` → erro 22P02 → **500** em vez de 404/negado para não-membros (e risco em qualquer policy avaliada nessa ordem). Funções agora tratam `''` como "não definido"; teste de regressão em conexão única reutilizada (falha antes, passa depois).
**Pendências P3:** entrega/ack reais dependem de telefone (P7); mídia inbound/outbound não coberta (P4 do plano WAHA antigo = mídia, ainda ⏳ — ver "Backlog pós-GOAL").

### P4 — Compose completo + runbook ✅ (clean-room provado)
**Entregue:** `docker compose up` sobe a stack inteira e funcional: `postgres`, `nats`, **`migrate`** (runner idempotente `tools/migrate-sql.sh`: `schema_migrations`, cada migration numa transação com seu registro, `BASELINE_UP_TO` para adotar bancos migrados à mão, aplica `OMNIRA_APP_DB_PASSWORD` em `omnira_app`), `api`, **`worker`**, `web` (nginx: SPA + `/api`, **SSE sem buffer/timeout de 1 h**, `/internal/*` → 404), `waha` (profile `whatsapp-unofficial`) e `seed` (profile `dev`, `tools/seed-dev.sql`, corrige o seed antigo que não casava com o schema). `nginx` de borda virou opcional (`--profile edge`; o antigo servia um SPA vazio). Runbook: `docs/ops/RUNBOOK-INBOX-WAHA.md`. `.env.example` completo.
**Prova:** `scripts/cleanroom-compose.sh` — projeto isolado `omnira-cr` (volumes novos, senhas/chaves aleatórias, portas livres, WAHA real na rede do compose): 27 migrations aplicadas pelo serviço, `omnira_app` autentica com a senha gerada, api/worker/web **healthy**, seed pelo serviço, SPA e `/api` pelo container `web`, `/internal` bloqueado e **e2e do navegador 11/11 através do nginx do compose** (inclui realtime SSE pelo proxy e QR real do WAHA).
**Defeitos encontrados e corrigidos no caminho:** (a) migration `000006` fixava `GRANT CONNECT ON DATABASE omnira_dev` → falhava em qualquer outro nome de banco (provado em cluster novo); agora usa `current_database()`; (b) healthcheck do `web` usava `localhost` (IPv6) e o do `waha` usava `wget` (imagem só tem `curl`; `/health` exige a chave → usa `/ping`); (c) compose apontava `web` sem porta e `nginx` quebrado; (d) `/internal/*` caía no fallback do SPA.
**Pendências P4:** `otel-collector` sem healthcheck funcional (imagem distroless) e sem coletor de destino configurado/validado; Postgres do compose expõe `55434` no host por padrão (restrinja em produção); TLS/edge só documentado (`omnira-nginx.conf` do host); sem CI que rode `cleanroom-compose.sh`; imagens sem scan/pin de digest.

### P5 — Contratos ✅
`contracts/openapi/omnira-v1.yaml` (OpenAPI 3.0.3, **14 paths / 16 operações**: login mock, inbox list/get, messages list/send (`Idempotency-Key`), assign/unassign, SSE tenant e conversa, channels WAHA create/list/get/start/stop/qr, webhook WAHA) e `contracts/asyncapi/omnira-v1.yaml` (AsyncAPI 2.6: `job.routing.assign.v1`, `job.channel.send_text.v1`, `inbox.events.{tenant}.{conversation}`; regras: só referências, `tenant_id` do envelope nunca autoriza). Documentam permissões, idempotência, 404 sem oráculo, reautorização do SSE e o que **não** é público (webhook e `/internal`).
**Validação:** Redocly lint = válido (0 erros/warnings); `tools/validate-specs.sh` agora aponta para esses arquivos e roda o lint semântico; **teste de drift** `internal/platform/httpserver/contract_test.go` usa o `ServeMux` real: toda operação documentada tem de casar com uma rota registrada, e toda rota inbox/channels do `server.go` tem de estar documentada (mutação provada nas duas direções).
**Pendências P5:** erros ainda `text/plain` (sem Problem Details/`correlation_id` como pede `docs/architecture/API-GOVERNANCE.md`); rotas de tenancy/membros/auditoria/relatórios/BPO/tools **não** estão no contrato (só o vertical Inbox/WAHA); esquemas de resposta não são validados contra o JSON real automaticamente (só rotas); Meta webhook (`/webhooks/v1/whatsapp/meta`) não documentado; sem geração de cliente TS a partir do contrato (o frontend usa tipos escritos à mão em `web/src/types/api.ts`).
