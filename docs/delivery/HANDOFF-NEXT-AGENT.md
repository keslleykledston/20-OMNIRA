# Handoff para o próximo agente

> **Comece aqui.** Estado em `master` (branch atual, **AUTH.0-AUTH.7 + CRM.1-4 COMPLETO**, nada foi enviado com push nem tag). Atualize este arquivo ao terminar sua sessão.
> Regras do dono do projeto: só perguntar em dúvida **real** (ordem lógica você decide); nunca `git push`/tag sem ordem; não declarar produção pronta; evidência antes de dizer PASS; respostas em português, diretas.

## 1. Situação em 5 linhas
- **GOAL TÉCNICO COMPLETO** (Inbox WhatsApp não oficial/WAHA operável com CRM mock): login (OIDC) → QR (PASS 6/6) → receber → assumir → responder → status → **abrir/atualizar/fechar ticket CRM** — multi-tenant com RLS, via `docker compose`.
- **STATE: FIRST_WHATSAPP_ATTENDANCE_NEARLY_COMPLETE** — QR gerado em w3-smoke (6/6 PASS), CRM backend integrado (6/6 unit tests PASS), CRM frontend UI agora rodando (TicketPanel.tsx em ConversationPage), testes e2e revalidando.
- **AUTH.0-AUTH.7 COMPLETO:** Opaque server-side sessions, OIDC, fail-closed production validation, 48/51 Go tests PASS.
- **CRM.1-4 COMPLETO:** MockCRMConnector (6/6 unit tests), HTTP handlers (create/get/update/close ticket), wired into httpserver com RLS, TicketPanel.tsx integrada ao ConversationPage.
- **Proxima fase:** CRM.5 (E2E com operador humano abrindo/fechando ticket real) → FIRST_INTERNAL_PRODUCT_DELIVERY.

## 2. Ordem de leitura (30 min)
1. `docs/delivery/ROADMAP-TO-GOAL.md` — fases P0–P6, o que foi achado/corrigido em cada uma, **pendências por fase** e **backlog em ordem**.
2. `docs/audit/GATE-INBOX-WAHA-LAB.md` — gate formal: evidência reproduzível, triagem da revisão Codex, **registro de dívidas D-1…D-5**, bloqueios.
3. `docs/architecture/INTEGRATIONS-TAB.md` — projeto da próxima entrega (catálogo por descritor, QR/parâmetros por plataforma, API proposta, fases I0–I5).
4. `docs/ops/RUNBOOK-INBOX-WAHA.md` — subir, operar, verificar, solução de problemas.
5. `CLAUDE.md` (projeto), `START-HERE.md`, `docs/architecture/TENANCY-SECURITY.md` — regras inegociáveis (tenant só do JWT, RLS, sem segredo em fila/log).
6. `docs/delivery/DELIVERY-SLICES.md` — histórico por slice (seção "Estado de execução").

## 3. Onde está cada coisa
| Preciso de… | Onde |
|---|---|
| Estado/pendências por fase | `docs/delivery/ROADMAP-TO-GOAL.md` |
| Gate, achados de segurança, dívidas | `docs/audit/GATE-INBOX-WAHA-LAB.md` |
| Contrato HTTP/SSE/webhook | `contracts/openapi/omnira-v1.yaml` (16 operações) |
| Contrato NATS/realtime | `contracts/asyncapi/omnira-v1.yaml` |
| Drift contrato×rotas | `internal/platform/httpserver/contract_test.go` |
| Migrations (000001–000027) | `migrations/*.up.sql` / `*.down.sql`; runner: `tools/migrate-sql.sh` |
| Seed de dev (usuários do login mock) | `tools/seed-dev.sql`; fixtures de e2e: `web/e2e/fixtures.sql` |
| Compose | `docker-compose.yml` (serviços `postgres nats migrate api worker web waha seed`; profiles `dev`, `whatsapp-unofficial`, `edge`) |
| Variáveis de ambiente | `.env.example` (comentado); config lida em `internal/platform/config/config.go` |
| Auth mock/OIDC | `internal/platform/authn/{mock_login,oidc,postgres}.go`; mock: `test@omnira.local` agente, `admin@omnira.local` admin |
| RBAC (permissions) | tabelas `permissions`/`role_permissions`; `conversation.claim`, `conversation.manage` (000023), `channel.manage` (000024) |
| Atribuição/claim atômico | `internal/routing/{application,adapters}/assign*.go` |
| Envio outbound + idempotência | `internal/messages/**`, worker: `internal/worker/delivery/**` |
| Conexões/sessão/QR WAHA | `internal/channels/application/waha_connections.go`, `internal/channels/adapters/{connections_http.go,waha/**}` |
| Webhook WAHA (HMAC-SHA512) | `internal/channels/adapters/waha/webhook.go` |
| Meta Cloud (preservado) | `internal/channels/meta/**` (webhook + parsing; config **global** via `OMNIRA_META_*`) |
| Realtime | triggers em `migrations/000026*`, ponte `internal/worker/realtime/bridge.go`, SSE `internal/inbox/adapters/sse.go`, hook `web/src/hooks/useRealtimeEvents.ts` |
| Guarda de role do banco | `internal/platform/db/role_guard.go` (API/worker recusam superuser/BYPASSRLS) |
| Frontend | `web/src/pages/{InboxPage,ConversationPage,IntegrationsPage}.tsx`, `web/src/lib/{session,config,integrations}.ts` |
| Testes vertical/E2E | `internal/e2e/vertical_test.go` (sem telefone), `web/e2e/*.spec.ts` (navegador) |
| Scripts de verificação | `scripts/cleanroom-compose.sh`, `scripts/e2e-inbox.sh`, `scripts/backup-restore-check.sh`, `scripts/w3-smoke.sh` |
| Memória do agente | `~/.claude/projects/-data-home-moved-Projects--legacy-lowercase-projects-20-OMNIRA/memory/` (`omnira-current-state.md`) |

## 4. Dados de ambiente (dev) — como obter
- **Containers do dev (não derrube):** `omnira-postgres` (porta `127.0.0.1:55434`) e `omnira-nats` (`4222`) já rodam. Bancos: `omnira_dev` (do dono; **desatualizado**, sem 000026/000027 nem `schema_migrations`), `omnira_test` (usado nos testes; pode ter dados residuais), `omnira_m044_diag`.
- **Roles:** owner `omnira` (senha dev `omnira`, **superuser/BYPASSRLS — só para migrations/seed**); aplicação `omnira_app` (senha dev `omnira_app`, **tem LOGIN**, criada na migration 000006). O runtime **deve** usar `omnira_app`.
- **`.env` local** (fora do git): `OMNIRA_DATABASE_URL` já foi trocado para `omnira_app`; backup do original em `/tmp/env.bak.omnira` (pode sumir). Chave de credenciais: `OMNIRA_CREDENTIALS_KEY` (base64 de 32 bytes) — gere com `head -c 32 /dev/urandom | base64`. **Não** imprima segredos do `.env`.
- **WAHA:** imagem `devlikeapro/waha:gows-2026.8.2` (local). Chave da API é texto puro, igual em `WAHA_API_KEY` (container) e `OMNIRA_WAHA_API_KEY` (cliente). Não use o container `deskcommcrm-waha` de outro projeto.
- **Host sem Go:** rode Go via docker:
  ```bash
  docker run --rm --network host -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false \
    -e OMNIRA_DATABASE_URL='postgres://omnira:omnira@127.0.0.1:55434/<db>?sslmode=disable' \
    -e OMNIRA_APP_DATABASE_URL='postgres://omnira_app:omnira_app@127.0.0.1:55434/<db>?sslmode=disable' \
    -e OMNIRA_NATS_URL='nats://127.0.0.1:4222' golang:1.25 go test -count=1 -p 1 ./...
  ```
  DB de teste: crie um banco novo no `omnira-postgres` e aplique `migrations/*.up.sql` como owner (ou use `tools/migrate-sql.sh`). Ao rodar sem essas variáveis, os testes de integração **dão skip** (não falham) — confira o número de skips.
- **Node/Playwright:** `web/node_modules` instalado; chromium do Playwright em `~/.cache/ms-playwright`.
- **Codex:** `codex` CLI existe, mas o sandbox de leitura **falha neste host** (`bwrap`). Para revisão, envie o código **inline** no prompt (`codex exec -s read-only --skip-git-repo-check -C /tmp - < bundle.txt`). Nunca declare "zero achados" sem saída real.
- **Portas ocupadas neste host** (não são do projeto): `8080` (processo do usuário), `9090` (métricas), `18080`. Use portas livres nos testes.

## 5. Como verificar rápido (o que rodar antes de dizer "PASS")
| Verificação | Comando | Esperado |
|---|---|---|
| Stack completa por compose + navegador | `scripts/cleanroom-compose.sh` | 12/12 e2e, headers de segurança |
| E2E navegador (containers avulsos) | `scripts/e2e-inbox.sh` | 12/12 |
| Backup/restore + RLS após restore | `scripts/backup-restore-check.sh` | `BACKUP/RESTORE VALIDATED` |
| Contratos | `tools/validate-specs.sh` | tudo OK |
| Web | `cd web && npx tsc --noEmit && npx vitest run` | 43 testes |
| Go (comando acima) | — | 0 falhas |
| WhatsApp real (humano) | `! scripts/w3-smoke.sh` (`--until-qr` = só a parte automática) | 6/6 automáticos + passos com telefone |
Toda mudança de migration: teste **up → down → up** e **down-all → up-all**.

## 6. O que fazer a seguir (ordem sugerida)
### Bloqueadores resolvidos / Próximas prioridades

**DECISION REQUIRED** — Escolha uma:

**Opção A:** Prosseguir I1 (refactor rota /channels → /integrations)
- Rápido: atualizar 3-4 rotas e ajustar links no frontend
- Prerequisito: testes Go ainda com falha de RLS (não impede I1, mas deixa suite "vermelho")
- **Sugestão:** fazer primeiro se quer feature completa I0-I1 antes de I2

**Opção B:** Fixar testes de Go (RLS INSERT)
- Necessário: corrigir tenancy, routing, authn, outbox adapters (6+ packages)
- Pattern: usar `app.is_system_admin` GUC em transações, ou `WithSystemTenantSession`
- Referência: `internal/worker/delivery/postgres_test.go` (delivery_test.go) e `publisher_integration_test.go`
- **Sugestão:** paralelo com I1, ou só se for prioritário para CI verde

**Opção C:** Integrar OIDC (D-3)
- Escalada: esboço feito, mas faltam: wiring de login, IdP real, cookie session
- Bloqueador de produção? SIM (D-3 em dívidas)
- **Sugestão:** só se IdP é disponível agora; caso contrário, deixar para fase posterior

---

### Tarefas definidas
1. **Entrega P7 humano** — `scripts/w3-smoke.sh --until-qr` passou 6/6; pedir proprietário para telefone descartável + completar pareamento/inbound/outbound/ack.
2. **IdP real (se disponível)** — cadastrar client/redirect em `OMNIRA_AUTH_*`, provisionar `users.external_subject`, testar login/refresh/logout.
3. **Integrações I2** — Meta por conexão somente após gate de segurança (webhook expose, rate limit, credentials no CredentialStore).
4. **Testes de Go** — quando contar: corrigir RLS INSERT nos 6+ packages falhando.
4. **Aplicar migrations no `omnira_dev`** do dono (pedir OK): usar `tools/migrate-sql.sh` com `BASELINE_UP_TO` no último ponto realmente aplicado; sem isso o `.env` local novo roda sem realtime e sem a correção 000027.
5. Dívidas do gate: D-1 (entrega por lease/reconciliação), D-2 (rate limit webhook/login), retenção do Outbox, Problem Details, métricas de negócio, mídia WAHA (`SendMedia`, download com allowlist SSRF).
6. Meta Cloud por conexão (I2 do doc) só depois de I0/I1.

## 7. Armadilhas já encontradas (não repita)
- **Nunca** rodar API/worker como `omnira` (superuser ignora RLS) — há trava de boot.
- `gofmt -w` na árvore inteira reformata arquivos alheios: formate só os seus (`gofmt -l` mostra o drift antigo).
- `set_config(..., true)` deixa o GUC como `''` na conexão do pool (corrigido em 000027); ao criar novas funções RLS, trate `''` como "não definido".
- Webhook sem sessão de tenant não lê a chave HMAC (RLS) — use `UseSession` (já ligado no `main` da API).
- Migrations não podem citar nome de banco fixo.
- Evento realtime carrega **só referências**; o corpo vem da API autorizada.
- nginx: `add_header` dentro de uma `location` anula os do `server` — use o snippet `web/security-headers.conf`.
- Fixtures de teste: chaves de idempotência têm 8–128 chars; parâmetros SQL repetidos com tipos diferentes dão `42P08`.
- `scripts/w3-smoke.sh` e `e2e-inbox.sh` usam portas/containers próprios e se limpam sozinhos; `--keep` deixa tudo de pé (limpe depois).

## 8. Limpeza / estado do repositório
Árvore limpa em `dd9e191`. Sem containers do projeto além de `omnira-postgres`/`omnira-nats`. Bancos residuais de teste: `omnira_test` (dados de smokes anteriores). Arquivos temporários ficam em `/tmp` (`/tmp/env.bak.omnira`, `/tmp/codex-*`) e no scratchpad da sessão.
