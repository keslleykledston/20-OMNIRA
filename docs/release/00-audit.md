# F0 — Auditoria real e baseline imutável

**Fase:** F0 (somente leitura, sem modificações no código)
**Data:** 2026-10-20
**Baseline SHA:** `714c1145b999b97628b64e24906b0c2fee53c55a` (branch `main`)
**Branch/worktree isolada:** `feat/omnira-release-update-engine` em `/opt/omnira/worktrees/release-engineering`
**Especificação normativa:** `docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md` (persistida nesta worktree)
**Status deste documento:** PASS — auditoria real executada, com citações `arquivo:linha`. Nenhuma alteração de código feita nesta fase.

---

## 0. Achado crítico de contexto operacional (não estava no escopo do F0, mas condiciona tudo)

Este host (`onmira-server`) **é produção real e limitada hoje**, não laboratório:

- Sessão WAHA do stack principal (`20-omnira-waha-1`, conexão `omnira_85af82d7-6f01-40cf-8d15-0e04df66736a`) está `WORKING`, conectada a um número real.
- `docs/operations/PILOT-RUNBOOK.md:3-6` chama esse estado de **`LIMITED_INTERNAL_PRODUCTION`** — vocabulário oficial do projeto, não "produção" genérica nem "lab".
- `docs/AI_HANDOFF.md` (untracked, 2026-10-19 21:01) descreve este mesmo host como "clone de migração com serviços desligados" — **está desatualizado**; o cutover já ocorreu. Não foi reescrito nesta fase (nota operacional de terceiros); sinalizado aqui para quem fizer manutenção do handoff.
- `docker-compose.prod.yml` **não é** a stack real de produção — é um stack de validação/lab com `OMNIRA_ENV=lab` e `OMNIRA_AUTH_MODE=mock` hardcoded (`docker-compose.prod.yml:14-18`). A stack real roda a partir de `docker-compose.yml` + `docker-compose.override.yml` (confirmado: `.env` tem `OMNIRA_AUTH_MODE=oidc`, `OMNIRA_ENV=development`).
- Existe um segundo stack paralelo, "headless" (`omnira-hl-*`, projeto `omnira-hl`), cujo WAHA tem zero sessões — não há risco de duplicação de envio hoje, mas **qualquer automação de update/deploy construída neste programa deve isolar explicitamente os dois stacks** (nomes de projeto, redes, volumes) e nunca reiniciar o container WAHA do stack principal sem necessidade (`PILOT-RUNBOOK.md:367-380`: reiniciar WAHA arrisca exigir novo QR code da conta real conectada).

**Consequência direta para todas as fases seguintes:** qualquer ambiente de staging/teste de falha (F8) e qualquer execução real de deploy (F3+) deve ser construído com isolamento comprovado (projeto/rede/volume próprios) deste stack ao vivo. Nenhuma ação deste programa reinicia `20-omnira-waha-1` ou `omnira-api`/`omnira-worker`/Nginx público fora de uma janela explicitamente autorizada.

---

## 1. Inventário de branches e worktrees (preservação de trabalho paralelo)

`git worktree list` no início do F0:

| Worktree | Branch | HEAD |
|---|---|---|
| `/opt/omnira/20-OMNIRA` (principal) | `main` | `714c114` |
| `/opt/omnira/worktrees/admin-console` | `feat/admin-console` | `d887cf0` |
| `/opt/omnira/worktrees/backend-buildfix` | `fix/backend-build-regressions` | `ee7d51e` |
| `/opt/omnira/worktrees/brand-logo-favicon` | `feat/brand-logo-favicon` | `f51937b` |
| `/opt/omnira/worktrees/flow-builder` | `feat/flow-builder` | `9da7cc4` |
| `/opt/omnira/worktrees/headless-crm` | `feat/headless-crm-dual-operation` | `b3c30a6` |
| `/opt/omnira/worktrees/headless-on-main` | `prep/headless-on-main` | `cdc1349` |
| `/opt/omnira/worktrees/idle-reminders` | `feat/menu-idle-reminders` | `e258296` |
| `/opt/omnira/worktrees/open-notice` | `feat/open-notice` | `b79c06a` |
| `/opt/omnira/worktrees/sector-scheduling` | `feat/sector-scheduling-overflow` | `8cb9329` |
| `/opt/omnira/worktrees/service-context` | `feat/omnira-service-context-operations` | `e1ae192` |
| **`/opt/omnira/worktrees/release-engineering` (nova, deste programa)** | `feat/omnira-release-update-engine` | `714c114` |

Nenhuma dessas worktrees/branches foi tocada, nenhum `git reset`/`clean`/rebase/push forçado executado. A árvore principal (`/opt/omnira/20-OMNIRA`) mantém, intocadas, suas próprias mudanças locais pendentes (`docker-compose.override.yml`, correções de OIDC em `web/src/pages/{Login,AcceptInvitePage}.tsx` e teste associado — trabalho de uma sessão de debug anterior, não deste programa).

---

## 2. Dois mecanismos de migration coexistindo — risco confirmado

A especificação já alertava para isso; confirmado por leitura direta:

1. **`tools/migrate-sql.sh`** (correto, canônico): idempotente, ledger `schema_migrations` (cria com `CREATE TABLE IF NOT EXISTS`), uma transação por migration (`psql -1 -f "$f"`), comandos `up`/`down <versão única>`/`status`, suporta adoção via `BASELINE_UP_TO`, e tem tratamento cuidadoso de segredo para `OMNIRA_SESSION_CTX_KEY` (ADR-0042): desliga logging de statement na sessão, passa o valor via bind param, recusa operar se `pgaudit`/`auto_explain` estiverem carregados sem opt-in explícito (`tools/migrate-sql.sh:70-88`). Este é o único runner que o Update Agent (F5) pode invocar.
2. **`make migrate-up`/`make migrate-down`** (Makefile:59-72): **perigoso**. `migrate-up` aplica **todos** os `migrations/*.up.sql` em loop via `cat | psql` sem checar `schema_migrations` — reaplicaria tudo de novo se rodado duas vezes. `migrate-down` reverte **todos** os `*.down.sql` em ordem reversa, não apenas o último aplicado. **Nunca deve ser invocado por automação de release/update** — confirma a proibição explícita da especificação (§A.10, §8) e de `CLAUDE.md`.

**Decisão para F1+ (não é ambiguidade, é consequência direta da auditoria):** todo código de release/update usa exclusivamente `tools/migrate-sql.sh status|up|down <versão>`; o Makefile não é tocado nem usado como dependência de automação (os alvos legados continuam existindo para uso manual/dev, sem alteração).

---

## 3. Backend Go — padrões reutilizáveis (auditoria completa em subagente, resumo aqui)

### 3.1 `platform_operators` **já existe** — não é preciso inventar RBAC de plataforma

- ADR-0038 (`docs/adr/0038-hub-platform-management.md:35-45`) já introduziu o conceito `platform_operator`, distinto de `system_admin` (modo interno de sessão) e de `hub_admin` (delegação de Hub).
- Tabela `platform_operators` (`migrations/000099_platform_operators.up.sql:8-25`): `user_id PK, status active|revoked, granted_by`; RLS forçada; política restringe `SELECT` a `is_system_admin() OR user_id = current_user_id()` — ninguém enumera outros operadores.
- Resolução **sempre server-side**, nunca de claim de token: `internal/hub/provisioning/operators.go:83-91` (`IsPlatformOperator`) chama `is_platform_operator($1)` no Postgres.
- Mutação **só via CLI** rodando como sessão de sistema: `omnira-hubctl platform-operator add|revoke|list` (`apps/hubctl/cmd/omnira-hubctl/main.go:42-43,343-353`) — comentário explícito no código: "there is deliberately no HTTP route, so nobody can promote themselves" (`operators.go:13`).
- Padrão de autorização de endpoint, pronto para copiar: `internal/hub/adapters/admin_http.go:34-59` (`AdminHandler.scope`) checa `IsPlatformOperator` e devolve **404 uniforme** em caso de negação (nunca 403) — mesmo estilo usado em todo acesso delegado do projeto.

**Decisão para F6:** os novos endpoints `/api/v1/platform/releases/*` e `/api/v1/platform/updates/*` são gated por `IsPlatformOperator`, reaproveitando a tabela/função/CLI existentes. Nenhuma tabela de autorização nova é necessária para "quem pode operar o updater" — só os dados da própria operação (planos, runs, eventos).

### 3.2 TenantContext / RLS — nunca aceitar `tenant_id` do payload

Três camadas, todas em código (não convenção): (1) HTTP só lê `tenant_id` da URL (`internal/tenancy/adapters/http.go:63`), nunca do corpo — DTOs de escrita nem têm esse campo e o OpenAPI usa `additionalProperties: false`; (2) mesmo o valor da URL só vira autoridade via `AuthorizationService.AuthorizeAccessToTenant` (`internal/tenancy/application/authorization.go:46-90`), que exige membership ATIVA no banco; (3) RLS reforça de novo independente de bug de app — políticas usam `current_user_id()` vindo de uma GUC assinada/`SET LOCAL` (`internal/platform/sessionctx/sessionctx.go:90-123`, ADR-0042), nunca de coluna influenciável pelo cliente.

`FORCE ROW LEVEL SECURITY` confirmado em `tenants, memberships, users, audit_events, outbox_events` (`migrations/000005_force_rls.up.sql:8-12`), estendido a `roles` (`000009`) e a tabelas de Hub/plataforma (`000094`, `000099:18-19`). Um teste de completude roda contra isso para toda tabela `tenant_id`-bearing futura: `internal/platform/db/rls_completeness_test.go:45-60`.

Runtime conecta como `omnira_app` (`NOSUPERUSER NOBYPASSRLS`, `migrations/000006_app_role.up.sql:8-14`), reforçado em boot por `internal/platform/db/role_guard.go:20-33` (`RequireUnprivilegedRole`), que recusa subir se `current_user` for superuser ou tiver bypass RLS.

### 3.3 `/internal/*` — isolamento é de rede, não de app

Sem middleware de auth em processo; proteção é só segmentação de rede (API só publica `127.0.0.1:8080`; Nginx público tem `location /internal/ { return 404; }` — confirmado em sessão anterior nesta mesma conversa, `/etc/nginx/sites-available/omnira`). Rotas são `mux.HandleFunc` simples (`internal/platform/httpserver/server.go:177-204`). **Implicação para F6:** os endpoints `/api/v1/platform/*` são rotas de negócio gated por papel (`IsPlatformOperator`), não rotas `/internal/*` — o padrão certo a copiar é o de `admin_http.go`, não o de isolamento de rede.

### 3.4 Idempotência, paginação, rate limit — template pronto

`internal/hub/adapters/admin_http.go:95-125` (`CreateCompany`): exige header `Idempotency-Key` (8-128 chars), devolve `200 + Idempotent-Replayed: true` em replay vs `201` na primeira vez. Paginação por cursor: `internal/platform/pagination/http.go:9-37` (`cursor/sort/limit`, headers `X-Pagination-*`). Rate limit pós-autorização: `internal/platform/ratelimit/middleware.go` + `EnforceTenantUser`/`EnforceUser`.

### 3.5 Audit log — tabela única, não uma por domínio

`audit_events` (`migrations/000003_audit_outbox.up.sql:4-21`, FORCE RLS) é a única tabela; visões "platform"/"hub" são **filtros** sobre a mesma tabela com whitelist de fatos (`internal/hub/auditlog/auditlog.go:48`), não tabelas novas. As próprias ações de `platform_operators` já são auditadas assim (`operators.go:41,58`, ações `platform.operator.added`/`platform.operator.revoked`).

Padrão de **append-only reforçado no banco** (mais forte que `audit_events`), pronto para o ledger `platform_update_events` do F6: `migrations/000087_append_only_tables.up.sql:5-12` — `REVOKE UPDATE, DELETE ... FROM omnira_app` + trigger que recusa `UPDATE`.

### 3.6 NATS/JetStream — padrão para o Update Agent

Stream único `OMNIRA_JOBS` (`internal/worker/jobsstream/config.go:17-65`, subject `job.>`, `LimitsPolicy`, `MaxAge=7d`, dedup 2min). Consumidor padrão: `internal/worker/routing/consumer.go:18-54` (durable, `AckExplicitPolicy`, `FilterSubject` específico tipo `job.routing.assign.v1`, `MaxDeliver=10`, `AckWait=30s`, mapeamento de erro para Ack/Term/Nak). **Reuso direto**: um consumidor `job.platform.update.apply.v1` segue exatamente este molde. Publicação sempre via outbox→NATS (`internal/worker/publisher/publisher.go:17-60`), nunca direto.

### 3.7 Observabilidade

OTel SDK oficial, OTLP/gRPC para traces e métricas (`internal/platform/otel/tracer.go:1-74`); middleware HTTP gera/propaga `X-Correlation-ID` e cria span por requisição (`internal/platform/otel/middleware.go:22-64`). Para contadores/gauges simples (`update_runs_total`, `agent_heartbeat_age` etc., conforme §11 da especificação), usar `internal/platform/opsmetrics`, já usado em `sessionctx.go:69` e `session_ctx_guard.go:13` — não introduzir um terceiro caminho de métricas.

### 3.8 Health checks existentes

`GET /healthz` (Postgres+NATS), `GET /internal/health/ready` (sem cache, roda a checagem de novo a cada chamada), `GET /metrics` (Prometheus). Healthchecks Docker: `pg_isready`, `nc -z`, `valkey-cli ping`, `wget` no `/healthz` da API.

---

## 4. Frontend (React + Vite, não Next.js) — onde a UI de plataforma entra

### 4.1 Divergência de documentação já conhecida, confirmada

`docs/architecture/PLATFORM-BASELINE.md:10` e `docs/architecture/DEPLOYMENT-NGINX.md:127-131` ainda descrevem Next.js (`NEXT_PUBLIC_APP_URL`). `web/package.json` e todo o código real usam **React + Vite + Vitest + Playwright**. Confirmando a instrução da especificação (§A.9): **não migrar a stack**; a divergência fica registrada aqui, correção documental é decisão de quem mantém esses dois arquivos — fora do escopo deste programa.

### 4.2 Console de administração já existe — ADR-0041, IMPLEMENTADA e IMPLANTADA EM LAB

Segunda entrada de build Vite, independente do SPA do agente: `web/admin.html` → `web/src/admin/AdminApp.tsx` (basename `/admin`), shell `AdminShell.tsx`. Guarda é só de exibição (`AdminShell.tsx:14`: "a guarda só decide o que MOSTRAR; o servidor decide cada requisição de novo"), nunca autoridade. Testado com Playwright real (14/14, Postgres+RLS real, `scripts/e2e-hub-browser.sh`) e Vitest (`AdminConsole.test.tsx`).

**Mas**: a autorização do console hoje é **100% por Hub** (`useMyHubs()`, capabilities `can_manage_access`/`can_manage_instances`) — não existe nenhum conceito de "platform operator" no frontend. A única pista são rótulos de auditoria (`web/src/lib/hub.ts:516-522`, strings `platform.operator.*`), sem tela, sem tipo, sem hook.

**Decisão para F7 (resolve a ambiguidade aparente via ADR-0038 + ADR-0041 combinados, sem precisar perguntar):** a tela "Administração global → Plataforma → Versões e Atualizações" entra **dentro do console existente** (`web/src/admin/`, nova seção de nav), mas sua visibilidade/autorização usa uma capability **nova e separada** de Hub — espelhando `useAccess()`'s regra de ouro (`web/src/lib/useAccess.ts:7-10`: nunca decidir por nome de papel, só por `can('chave')` vindo do servidor). Isso requer um novo endpoint leve tipo `GET /me/platform-access` (ou campo equivalente em `/me`) que reflita `IsPlatformOperator` (§3.1) — não reaproveitar `useMyHubs()`, que é semanticamente "administra ESTE Hub", um conceito mais estreito e por instância.

### 4.3 Padrões reaproveitáveis já maduros

- Cliente de API por feature, tipado, com `describeXError` (`web/src/lib/hub.ts`), consumido via TanStack Query — não a instância `api.ts` genérica.
- **SSE com reconexão já pronto para progresso de update**: `web/src/hooks/useRealtimeEvents.ts` (fetch-stream por causa do header `Authorization`, backoff até 15s, `onReconnect` para refetch de estado perdido) + variante de poll como rede de segurança (`useHubEvents.ts`).
- Primitivos de design: `Table`, `Modal`/`ConfirmDialog` (exatamente o gate de confirmação do F7), `Badge`/`StatusBadge`, `Card`, `Tabs`, `LoadingState`/`ErrorState`/`EmptyState`. Sem `Stepper` genérico, mas `WizardStepper.tsx` é o padrão já usado (ex.: `WahaWizardPage.tsx`) para fluxos "configurar → QR → conectado" — mesmo molde para "preflight → confirmar → progresso → histórico".
- Testes: Vitest com `vi.mock('axios')` + `testUtils.renderAt`/`setSession` (`CompaniesPanel.test.tsx`); Playwright mock com `page.route()` interceptando `/api/v1/**`, afirmando corpo e `Idempotency-Key` (`hub-companies.mock.spec.ts`). Breakpoints canônicos: 1440/1024/768/390, limite real em `lg:` (1024px), sem overflow horizontal (`responsive-smoke.spec.ts`).

---

## 5. Deploy, volumes e backup — realidade vs. aspiração

### 5.1 `docker-compose.prod.yml` é uma armadilha confirmada

Citação exata (`docker-compose.prod.yml:1-18`): comentário do cabeçalho diz "Stack de validação do host devops... o frontend NÃO vem daqui", e o bloco `x-runtime` tem `OMNIRA_AUTH_MODE: mock` / `OMNIRA_ENV: lab` **hardcoded**, não herdado de `.env`. A stack real (confirmada: `.env` tem `OMNIRA_AUTH_MODE=oidc`) roda do `docker-compose.yml` base + `docker-compose.override.yml`. **Nenhuma automação deste programa deve tratar o nome do arquivo como garantia de ambiente** — exatamente o que a especificação (§A.10, item "Host de laboratório usa arquivo prod.yml") pedia para verificar.

### 5.2 Volumes com dados que precisam de backup

| Volume/mount | Serviço | Criticidade | Backup hoje |
|---|---|---|---|
| `postgres_data` | postgres | crítico (fonte de verdade) | **Sim** — `scripts/backup-omnira-db.sh`, cron horário (ver §5.3) |
| `keycloak_postgres_data` | keycloak-postgres | crítico (usuários/realm) | **Não** — nenhum script cobre |
| `nats_data` (JetStream) | nats | importante | **Não** — só aspiracional em `DISASTER-RECOVERY.md` |
| `waha_sessions`, `waha_media` | waha | crítico (sessão WhatsApp real) | **Não** — nenhum script cobre |
| `/opt/omnira-media` (bind mount) | api/worker | crítico | **Sim** — `scripts/backup-omnira-media.sh`, cron horário, só `clean/` |
| `valkey` | valkey | nenhuma (sem volume, comentário explícito "ephemeral, nunca fonte de verdade") | n/a |
| `clamav_db` | clamav | nenhuma (recriável via freshclam) | n/a |

### 5.3 Backup real já existe e está testado — não é construir do zero

- `scripts/backup-omnira-db.sh`: `pg_dump -Fc`, checagem de tamanho suspeito (<1KB falha), `.meta.json` irmão, retenção 7 dias, cópia best-effort para disco USB externo + rclone criptografado.
- `scripts/backup-omnira-media.sh`: incremental, checksum, só `clean/` (nunca quarentena), retenção 90 dias.
- `scripts/backup-restore-check.sh` / `backup-restore-hub-check.sh`: restauram contra bancos **descartáveis** (`omnira_bkp_restored`), nunca tocam o banco vivo.
- Cron do host (`docs/operations/PILOT-RUNBOOK.md:85-115`): `5 * * * * ... backup-omnira-db.sh`. **Verificado nesta auditoria:** `crontab -l | grep backup` não retornou nada neste host — **o agendamento não sobreviveu à migração do host** (consistente com `AI_HANDOFF.md` descrever um clone recente). `backups/omnira_dev/` também não existe neste checkout. **Achado F0, não corrigido nesta fase** (F0 é só leitura): o RPO de 1h declarado no runbook não está sendo cumprido neste host agora.
- Mesmo achado para `scripts/waha-session-check.sh` (cron de monitoramento a cada 5 min, `PILOT-RUNBOOK.md:439-474`): `crontab -l` vazio para esse script também.

### 5.4 Gap de governança: Helm/Kubernetes sem ADR

Existe um chart Helm completo (`helm/omnira/`: deployments, HPA, NetworkPolicy, Ingress, RBAC) e dois documentos (`docs/KUBERNETES.md`, `docs/HELM-PRODUCTION.md`) descrevendo deploy em Kubernetes como se fosse atual. **Nenhum ADR em `docs/adr/` menciona Kubernetes ou Helm.** Isso contradiz diretamente `CLAUDE.md`: "não introduza Kubernetes/Temporal/Kafka sem ADR aprovado". Único commit relacionado (`058af5d`) tem mensagem sobre outra coisa, sugerindo inclusão incidental, não decisão revisada. `values-prod.yaml` do chart assume "banco/NATS gerenciados externos", o que contradiz a topologia real (Postgres/NATS em container no mesmo host).

**Tratamento neste programa:** o Update Agent/Deploy Adapter (F5) usa exclusivamente o adapter Docker Compose, conforme a especificação manda (§2.2: "preferir Docker Compose... não impor Kubernetes sem ADR de necessidade demonstrada"). O chart Helm não é referenciado, não é usado como exemplo, e não é removido nesta fase (decisão de arquivar/formalizar não é deste programa — só o sinalizo).

### 5.5 `MANIFEST.json` confirmado como índice de pacote de docs

Array plano de `{path, sha256, bytes}` cobrindo `.agents/skills/*`, docs raiz e a maior parte de `docs/`. **Não** inclui `docker-compose*.yml`, `helm/`, `scripts/` nem código-fonte. Não é um release manifest de software — confirma a suspeita da especificação. O schema de release manifest do F1 é um artefato novo, sem relação com este arquivo.

### 5.6 `docs/operations/PILOT-RUNBOOK.md` é a fonte de verdade operacional

751 linhas, procedimentos **verificados de ponta a ponta neste ambiente** (PILOT.1–PILOT.3/PILOT.4C, 2026-09-26), ao contrário de `docs/architecture/DEPLOYMENT-NGINX.md` (genérico/desatualizado) e `docs/operations/DISASTER-RECOVERY.md` (declaradamente aspiracional). Contém: smoke test de login real, causas conhecidas de falha de OIDC (inclusive a classe de bug "401 invalid identity token com token exchange OK" que já foi corrigida uma vez — relevante para F5/F8), mecanismo de backup real (§5.3), drift de rede Docker do WAHA já observado uma vez, e o endpoint read-only de status de sessão WAHA (`GET /api/v1/tenants/{id}/channels/waha/connections/{id}`) que nunca reinicia nem reconecta — **modelo exato para o "External provider status" que o Update Agent deve respeitar em F8 (nunca acoplar saúde do WAHA a restart/liveness da aplicação)**.

---

## 6. Documentos canônicos — presença confirmada

| Documento | Status |
|---|---|
| `START-HERE.md` | existe |
| `CONTEXT.md` | existe |
| `CLAUDE.md` | existe |
| `AGENTS.md` | existe |
| `CODEX.md` | existe |
| `docs/product/MVP.md` | existe — escopo P0 não inclui release/update management (é trabalho de plataforma/ops aditivo, não altera o MVP do produto) |
| `docs/AI_HANDOFF.md` | existe, **desatualizado** (ver §0) |
| `docs/AI_WORKFLOW.md` | **ausente** — não inventado, apenas registrado |
| `MANIFEST.json` | existe — ver §5.5 |
| `tools/migrate-sql.sh` | existe — ver §2 |
| `.github/workflows/` | **ausente** — confirmado, nenhum workflow de CI existe hoje neste checkout |
| `docs/adr/` | 49 ADRs (`0017`–`0049`), próximo número livre: **0050** |

---

## 7. Ferramentas disponíveis neste ambiente

| Ferramenta | Disponível? | Nota |
|---|---|---|
| `git worktree` | sim | usado para isolar este programa |
| `docker`/`docker compose` | sim, via `sudo -n` (regra de sudoers escopada concedida nesta sessão) | sem acesso direto sem sudo |
| `codex` CLI | **sim** — `codex-cli 0.163.0` instalado | disponível para revisão independente (§A.4 da especificação) |
| `gh` CLI (GitHub) | **não instalado** | criação de PR/gestão de Actions fica `BLOCKED_EXTERNAL` até instalação/credencial — ver §8 |
| Credenciais GHCR/push | não verificadas/não presentes nesta sessão | builds OCI (F3) ficam `BLOCKED_EXTERNAL` para publicação real; build local sem push é possível |

---

## 8. Mapa de riscos e bloqueios conhecidos (para as fases seguintes)

| Risco/bloqueio | Fase afetada | Classe |
|---|---|---|
| Ambiente é `LIMITED_INTERNAL_PRODUCTION` real, com WhatsApp conectado de verdade | F8, F3+ | **Exige isolamento rígido de qualquer staging/teste de falha; nunca reiniciar WAHA do stack principal sem necessidade** |
| `gh` CLI ausente, sem credencial GHCR confirmada | F2, F3, F11 | `BLOCKED_EXTERNAL` — contratos/scripts/testes locais seguem; publicação real não |
| Cron de backup do banco e de monitoramento WAHA não sobreviveu à migração do host | F4, F8 | Achado operacional, não deste programa corrigir sozinho — documentar e recomendar reinstalação ao operador |
| `helm/omnira/` sem ADR, contradiz CLAUDE.md | F1 (ADR), geral | Não usar como referência; não remover nesta fase (não é escopo F0, é decisão do dono do repositório) |
| `make migrate-up`/`migrate-down` são perigosos e coexistem com o runner correto | F4, F5 | Nunca invocados por automação; `tools/migrate-sql.sh` é o único runner usado |
| Divergência Next.js (docs) vs React/Vite (código real) | F1, F7 | Já resolvida pela própria especificação: não migrar, registrar divergência (feito aqui) |
| `docs/ops/MEDIA.md` contradiz a si mesmo (L29 "backup pendente" vs L38-45 "já implementado") | F10 (docs) | Script real existe (`backup-omnira-media.sh`); texto do doc está desatualizado — corrigir em F10, não aqui |

---

## 9. Conclusão do F0

**PASS.** Nenhuma modificação de código feita. Baseline, riscos, arquitetura real e padrões reutilizáveis mapeados com evidência `arquivo:linha`. As descobertas mais importantes — `platform_operators` já existente, backup real (ainda que com cron caído neste host), console admin já existente (ADR-0041), runner de migration correto já existente, chart Helm não sancionado — mudam significativamente o escopo de F1–F7: este programa é majoritariamente **integração e extensão de infraestrutura real já existente**, não construção do zero.

**Próxima fase:** F1 — ADR do sistema de release/update (usará `ADR-0054`), `release/components.yaml` alinhado ao vocabulário já definido em `docs/architecture/MODULE-MANAGEMENT.md`, schemas JSON do release manifest.
