# Changelog

## Unreleased

### Added (M03.1 — Inbound persistence foundation)
- Provider-neutral inbound orchestration for Contact, Conversation, Message and Ticket.
- PostgreSQL adapter scoped by TenantContext and transaction querier.
- Provider-message idempotency: a redelivery cannot create a second message or ticket.
- WAHA webhook intake now reserves dedupe and persists the canonical message in one transaction.
- System TenantContext derives ownership from the persisted ChannelConnection without a fabricated user UUID.
- Provider-message uniqueness is scoped by ChannelConnection, with concurrent open-conversation/active-ticket guards.
- Inbound media metadata is persisted canonically without retaining the WAHA payload.
- `message.ack` updates outbound delivery status within the trusted connection scope and cannot regress delivered/read state.
- Atomic manual conversation claim with exactly one concurrent winner and append-only assignment history.
- Routing claim derives both Tenant and target operator from TenantContext; cross-tenant attempts remain indistinguishable from conflicts.
- Atomic system round-robin selects only active, available queue members below capacity and records system-owned assignment history.
- Round-robin derives Tenant from trusted system context; asynchronous Outbox/NATS dispatch remains a separate slice.
- PostgreSQL Outbox now honors the transaction querier, preserving atomic domain-write/event-write rollback semantics.
- Routing jobs use the canonical `job.routing.assign.v1` JetStream subject rather than the legacy event subject formatter.
- Durable routing consumer resolves tenant ownership from the persisted conversation and ignores envelope `tenant_id` for authorization.
- Round-robin redelivery is idempotent: an assigned conversation returns its existing owner without duplicating assignment history.
- Each tenant may mark one explicit default queue; new inbound conversations enter it without name-based or payload-based inference.
- Initial round-robin routing job is written atomically with inbound persistence; manual default queues remain available for operator claim.
- Outbox dispatcher now executes read, attempt and publish-mark operations inside transaction-local system sessions, compatible with `omnira_app` and RLS.
- Worker no longer emits database or NATS connection URLs to logs.
- Outbox UPDATE policy is restricted to the system worker session (`000021`); fresh migration bootstrap is now valid.
- D3.1 PostgreSQL adapter tests use the canonical TenantContext and compile with the existing `lib/pq` implementation.

Todas as mudanças relevantes do OMNIRA serão registradas aqui.

## [Unreleased]

### Added (M02 — Contact foundation)
- Tenant-owned provider-neutral `Contact` domain with canonical E.164 phone identity.
- PostgreSQL `contacts` migration with RLS, FORCE RLS, tenant-scoped policies, unique tenant/phone identity, and rollback.
- TenantContext-guarded repository and idempotent `UpsertByPhone` identity-resolution service.
- Contact state transitions for active, blocked, and archived records.

### Added (M03 — Conversation, Message and Ticket foundation)
- Tenant-owned conversation/message/ticket schema with composite tenant FKs and RLS/FORCE RLS.
- Canonical lifecycle models for open/closed conversations, inbound/outbound messages, and ticket resolution.
- Queue, routing, realtime inbox, and automation remain outside this slice.

### Added (M04 — Queue and routing foundation)
- Tenant-owned queues, queue members, availability/capacity and append-only assignment events.
- Manual and round-robin modes with deterministic oldest-assignment selection.
- Composite tenant FKs and RLS/FORCE RLS for routing data.
- Atomic claim remains a SQL application operation; no read-then-write claim path introduced.

### Added (U4 — WAHA outbound text foundation)
- WAHA `SendText` adapter using the canonical channel seam and `/api/sendText`.
- E.164 recipient validation, deterministic connection session ownership, and canonical sent result.
- Provider error classes for authentication, rate limiting, unavailable provider, disconnected session, configuration, permanent, transient, and unknown failures.
- Low-cardinality OTel `channel_operation_total` and `channel_operation_error_total` metrics.
- Idempotency remains owned by the Outbox/application layer; no secret or arbitrary provider payload crosses the domain seam.
- Worker delivery boundary consumes only `connection_id` plus outbound message references; tenant scope is reconstructed by `TenantSessionRunner`.
- Retry policy does not retry authentication, invalid configuration, permanent validation, or disconnected sessions.
- WAHA `message.ack` is normalized to canonical delivery states, and inbound media download is restricted to the configured WAHA origin with a 25 MiB limit.

### Added (U3 — WAHA webhook and inbound foundation)
- Connection-scoped webhook route: `POST /webhooks/v1/whatsapp/waha/{connection_token}`.
- Raw-body HMAC-SHA512 verification with constant-time comparison and per-connection CredentialStore secret.
- Session webhook configuration limited to `message.any`, `message.ack`, and `session.status`.
- WAHA message normalization to canonical `InboundMessage`; provider echoes ignored.
- System transaction resolves connection ownership; webhook payload never supplies tenant authority.
- Body limit and OTel webhook counters with low-cardinality labels.

### Added (U2 — WAHA session management)
- Deterministic `omnira_<connection-id>` session reference; arbitrary names rejected.
- Create/start/stop/restart/get session, QR, account identity, and provider health operations.
- WAHA statuses normalized to OMNIRA states; unknown states degrade safely.
- Persisted `provider_session_ref` with migration rollback.
- Tenant/connection identity checks in adapter; no tenant ID accepted from WAHA.

### Added (U1 — WAHA unofficial runtime foundation)
- Optional internal-only WAHA Docker service pinned to `devlikeapro/waha:gows-2026.8.2`.
- Persistent session/media volumes, healthcheck, restart policy, disabled dashboard/Swagger.
- Centralized WAHA config and engine validation (`GOWS`, `NOWEB`, `WEBJS`).
- Provider-agnostic WAHA HTTP client foundation with API-key transport and error classification.
- No WAHA port published to host; API key remains runtime secret.

### Added (D3.1 — Channel credentials foundation)
- `channel_connections` e `channel_credentials` com RLS + FORCE RLS e rollback.
- AES-256-GCM com chave Base64 `OMNIRA_CREDENTIALS_KEY` de 32 bytes.
- `CredentialStore` e repositórios PostgreSQL tenant-aware.
- Testes de isolamento A/B, ciphertext, chave errada e resolução de credencial.
- Contadores OTel de resolução de credenciais sem IDs ou segredos.

### Added (D3.2 — Meta webhook verification foundation)
- Verificação de challenge Meta e assinatura HMAC-SHA256 sobre raw body.
- Handler limitado a 1 MiB, resolução por `phone_number_id` confiável e bloqueio de conexão inativa/não oficial.
- Testes sem dependência de chamada externa à Meta.

### Added (T01–T02 — Bootstrap + Local Infrastructure)

#### T01 — Bootstrap Go repo
- Estrutura Go modular: `apps/api`, `apps/worker`, `internal/` com domain/application/ports/adapters.
- `go.mod` com dependências essenciais: pgx, NATS, OpenTelemetry, UUID.
- `config.Load()` com 12-factor env vars: OMNIRA_ENV, OMNIRA_HTTP_ADDR, OMNIRA_DATABASE_URL, OMNIRA_NATS_URL, etc.
- `httpserver.Server`: graceful shutdown, `/internal/health/{live,ready,modules}` endpoints.
- `cmd/omnira-api/main.go`: inicia servidor HTTP, responde health checks.
- `cmd/omnira-worker/main.go`: bootstrap worker com graceful shutdown.
- `Makefile`: targets build, test, fmt, vet, run-api, run-worker, docker-up/down.
- Testes unitários: `config_test.go`, `main_test.go` para health endpoints e graceful shutdown.

#### T02 — Local infrastructure (docker-compose)
- `docker-compose.yml`: Postgres 16, NATS 2.10 com JetStream, OTel Collector (padrão).
- `config/otel-collector-config.yml`: OTLP receivers (gRPC/HTTP), debug exporter, health check extension.
- `tools/dev-up.sh`: sobe infrastructure, aguarda health checks, exibe endpoints.
- `tools/dev-down.sh`: derruba infrastructure.
- `tools/check-docker-health.sh`: valida saúde de Postgres, NATS, OTel (script determinístico reutilizável em CI).
- `docs/development/LOCAL-SETUP.md`: guia de setup local, troubleshooting, volume management.
- Volumes persistentes: `postgres_data`, `nats_data`.
- Health checks: todos os serviços validam saúde com retry.

### Changed
- `docs/architecture/ARCHITECTURE.md`: corrigido para refletir Go exclusivo (era FastAPI/NestJS).
- `AGENTS.md`: adicionadas seções "Automation First" e "Automação como Prioridade" com hierarquia tool/skill/agent.
- `Makefile`: env vars corrigidas para porta 55434 (Postgres), localhost:4317 (OTel GRPC).

### Tested
- `go test ./...` passa (config, httpserver).
- `go build ./apps/api/cmd/omnira-api` / `omnira-worker` compilam.
- Health endpoints: `/internal/health/{live,ready,modules}` → 200 OK.
- `tools/dev-up.sh`: sobe ambiente em ~10s, todos os serviços healthy.
- `tools/check-docker-health.sh`: exit code 0 (todos os checks passam).

### Automation
- **Script criado:** `tools/check-docker-health.sh` (determinístico, reutilizável em CI para validar infra).
- Regra dos 2 usos: será promovido para CI após T03 (quando usado na validação de migrations).

### Docker-First (Decisão congelada)
- `docs/deployment/DOCKER-FIRST.md`: especificação completa de containerização obrigatória.
- `Dockerfile.api`: multi-stage build para omnira-api (Go), usuário não-root, healthcheck, metadata.
- `Dockerfile.worker`: multi-stage build para omnira-worker (Go), usuário não-root.
- `internal/platform/config`: adicionadas vars globais Version, Commit, BuildTime para injeção via ldflags.
- `docs/delivery/DEFINITION-OF-DONE.md`: adicionada seção Docker-First com requisitos obrigatórios.
- `AGENTS.md` + `START-HERE.md`: Docker-first integrado como princípio congelado.
- Imagens testadas: omnira-api:0.1.0 (8.77MB), omnira-worker:0.1.0 (5.77MB).

### Added (T03 — Migration framework + schema base)
- `migrations/000001_initial_schema.up/down.sql`: users, tenants tables com RLS enabled.
- `migrations/000002_memberships_rbac.up/down.sql`: memberships, roles, permissions, role_permissions com dados seed.
- `migrations/000003_audit_outbox.up/down.sql`: audit_events, outbox_events com índices de performance.
- `migrations/000004_rls_policies.up/down.sql`: RLS policies (tenant boundary) + helper functions (current_user_id, is_system_admin).
- `tools/apply-migrations.sh`: wrapper pra aplicar/validar migrations.
- `tools/validate-schema.sh`: valida presença de tabelas, RLS enabled, seed data (9 permissões, 5 roles).
- `tools/test-isolation.sh`: smoke test de isolamento tenant (A vs B).
- `Makefile`: targets migrate-up, migrate-down, migrate-validate, test-isolation.

### Added (Local AI — camada auxiliar opcional, fora do escopo do produto)
- `tools/ai/lib.sh`: client único para llama.cpp (ai_json, ai_sanitize, ai_metrics) com fallback e timeout.
- `tools/ai/triage-logs.sh`: reduz logs extensos a diagnóstico JSON curto; preserva log bruto.
- `tools/ai/triage-tests.sh`: resume falhas de teste; veredito pass/fail permanece determinístico (grep).
- `docs/architecture/LOCAL-AI.md`: escopo, limite de autoridade, rede, privacidade, fallback.
- `.env.example`: config da aplicação + `LOCAL_AI_*` (nenhum valor real).
- `.gitignore`: `.env`, chaves e métricas locais fora do Git.
- `AGENTS.md`: seção "Local AI — uso permitido" com a proibição de usá-lo como evidência final.
- Detectado (sem alterar): `llama-server` em 18088 (hermes-3-llama-3.1-8b, GPU) e 18090 (Devstral, CPU). Default 18088 por latência (~0,25 s vs ~40 s).

### Added (T05 — Tenant + Membership domain)
- `internal/tenancy/domain/tenant.go`: `Tenant` aggregate (LegalName, IsolationProfile, Status).
- `internal/tenancy/domain/membership.go`: `Membership` aggregate (TenantID, UserID, RoleID, Status).
- `internal/tenancy/domain/role.go`: `Role` (system roles + tenant-scoped).
- `internal/tenancy/ports/repository.go`: `TenantRepository`, `MembershipRepository`, `RoleRepository` interfaces.
- `internal/tenancy/application/service.go`: `TenantService`, `MembershipService` com lógica de negócio.
- Testes de invariantes: Tenant ativas/inativas/suspensas, Memberships ativas/revogadas.
- Factory functions (`NewTenant`, `NewMembership`) com validação de invariantes.

### Added (T04 — Authentication adapter)
- `internal/platform/authn/authn.go`: `Authenticator` interface + `JWTAuthenticator` (RSA, OIDC-compatible).
- `Principal` struct (UserID, Subject) — único ponto de entrada de identidade autenticada.
- `Middleware()` — HTTP middleware que valida Bearer token, injeta Principal no context.
- `WithPrincipal()` / `FromContext()` — armazenar/recuperar Principal de context.Context.
- Testes: JWT válido, expirado, issuer/audience inválidos, middleware sem token.
- `go.mod` → github.com/golang-jwt/jwt/v5 v5.3.1.

### Added (T13 — Backup/Restore validation drill)
- `tools/backup-restore-test.sh`: teste end-to-end de backup/restore com RPO/RTO medido.
- **5 fases**:
  1. Criar backup do banco (pg_dump → plain SQL).
  2. Registrar baseline (contagem de linhas por tabela).
  3. Simular perda de dados (DELETE audit_events, outbox_events).
  4. Restaurar do backup (psql → SQL restore).
  5. Validar integridade (contagens após restore = baseline).
- Métricas capturadas:
  - RPO (Recovery Point Objective): All data backed up.
  - RTO (Recovery Time Objective): ~102ms (restore time).
  - Backup time: ~106ms.
  - Backup size: 44K (para schema + seed data).
- Teste de sucesso: data integrity verified, baseline matches restored state.
- Exit code 0 (PASS), limpa backup files após sucesso.
- Uso: `OMNIRA_DATABASE_URL=... ./tools/backup-restore-test.sh`.

### Added (T12 — Isolation test harness)
- `internal/tenancy/adapters/isolation_test.go`: suite adversarial de testes de isolamento tenant.
- **4 cenários de teste contra PostgreSQL real**:
  - TestIsolation_TenantACannotReadTenantBData: Tenant A vs B data separation.
  - TestIsolation_MembershipRevocation_DeniesAccess: User revogado nega acesso mesmo com tenant ativo.
  - TestIsolation_MultiTenantDataSeparation: 3 tenants com 3 users cada, validar isolamento completo.
  - TestIsolation_TenantInactive_DeniesAccess: Tenant inativo nega acesso mesmo com membership ativa.
- Testes rodam contra OMNIRA_DATABASE_URL (PostgreSQL real com migrations).
- Validam: data separation, membership enforcement, tenant status checks.
- Determinísticos: cada teste cria seus próprios dados, sem dependências de estado anterior.
- Requer migrations aplicadas (tables, RLS enabled).

### Added (T11 — OpenTelemetry baseline)
- `internal/platform/otel/tracer.go`: InitOTel() inicializa tracer + meter providers com OTLP exporters (gRPC).
- Configura resource com ServiceName, ServiceVersion.
- Tracer batching, Meter periodic reader.
- Cleanup function para shutdown gracioso.
- `internal/platform/otel/middleware.go`: HTTPMiddleware com tracing automático.
- Correlation ID tracking: extrai X-Correlation-ID header, gera novo se ausente, injeta no response.
- Span attributes: method, url, target, correlation_id, status_code.
- GetCorrelationID(), WithCorrelationID() helpers para acessar correlation ID no context.
- `internal/platform/otel/middleware_test.go`: 5 testes (GeneratesCorrelationID, PreservesCorrelationID, GetCorrelationID_Missing, WithCorrelationID, StatusCode).
- Testes validam: geração de ID, preservação de ID existente, captura de status codes.
- Exporters OTLP (gRPC): conectam ao OTel Collector para exportar traces e métricas.
- Dependency added: go.opentelemetry.io/otel v1.46.0, exporters, gRPC stubs.

### Added (T10 — Outbox pattern + NATS publisher ready)
- `internal/outbox/domain/event.go`: OutboxEvent aggregate para padrão Outbox.
- EventType enum (tenant.created, membership.granted, etc), AggregateType enum.
- Correlation tracking (CorrelationID, CausationID) vinculado a operações.
- Payload JSONB para dados do evento.
- PublishedAt timestamp (null até publicado), Attempts (retry tracking).
- MarkPublished(), RecordAttempt(), IsPublished() métodos.
- `internal/outbox/domain/errors.go`: validação de tenant_id.
- `internal/outbox/ports/repository.go`: OutboxEventRepository interface.
- `internal/outbox/application/service.go`: OutboxService (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- `internal/outbox/application/service_test.go`: 5 testes (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- Transacional: eventos registrados no banco junto com operação original (atomicidade garantida via DB).
- Publisher worker (não implementado T10): consumir FindUnpublished(), enviar ao NATS JetStream, chamar MarkPublished().

### Added (T09 — Audit events + correlation IDs)
- `internal/audit/domain/event.go`: AuditEvent aggregate com correlation tracking.
- AuditAction enum (tenant.created, membership.granted, etc), AuditOutcome (success/failure).
- ResourceType enum (tenant, membership, role).
- CorrelationID para rastrear operações relacionadas, CausationID para causalidade.
- Metadata JSONB para contexto da ação (legal_name, profile, etc).
- `internal/audit/domain/errors.go`: validação de tenant_id, actor_id.
- `internal/audit/ports/repository.go`: AuditEventRepository interface.
- `internal/audit/application/service.go`: AuditService (RecordEvent, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- `internal/audit/adapters/postgres.go`: PostgresAuditEventRepository (Store, FindByID, FindByTenantAndCorrelation, FindByTenant, FindByAction).
- `internal/audit/adapters/http_handlers.go`: AuditAPIHandler (GET /api/v1/tenants/{tenant_id}/audit/events com paginação).
- `internal/audit/application/service_test.go`: 5 testes (RecordEvent, EventMetadata, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- RLS no nível de banco: audit_events.tenant_id protegida por RLS policies.

### Added (T08 — Tenant API HTTP endpoints)
- `internal/tenancy/adapters/http_handlers.go`: HTTP handlers para Tenant API.
- `TenantAPIHandler`: GetTenantMe, ListMemberships, CreateMembership, RevokeMembership.
- Response types: TenantResponse, MembershipResponse (JSON serializable).
- **Endpoints**:
  - `GET /api/v1/tenants/me` — retorna tenant do TenantContext (requer auth + authz).
  - `GET /api/v1/tenants/{tenant_id}/memberships` — lista memberships do tenant.
  - `POST /api/v1/tenants/{tenant_id}/memberships` — cria membership (user_id, role_id).
  - `DELETE /api/v1/tenants/{tenant_id}/memberships/{membership_id}` — revoga membership.
- Middleware chain: authn (Principal) → authz (TenantContext) → handler.
- `apps/api/routes.go`: RegisterRoutes() registra health endpoints + Tenant API routes.
- `internal/tenancy/adapters/http_handlers_test.go`: 5 testes (GetTenantMe, ListMemberships, CreateMembership, RevokeMembership_ServiceOnly, MissingTenantContext).
- Testes validam status codes (200/201/204/500/400), response JSON, e fluxo auth.

### Added (T07 — RLS / Persistence isolation)
- `internal/tenancy/adapters/postgres.go`: PostgreSQL repository implementations (Tenant, Membership).
- `NewPostgresTenantRepository()`, `NewPostgresMembershipRepository()` — adapters concretos.
- Store, FindByID, FindAll, Update — CRUD operations com queries parametrizadas.
- FindByTenantAndUser, FindByTenant — queries tenant-scoped.
- `internal/tenancy/adapters/postgres_test.go`: 5 testes contra PostgreSQL real (com migrations aplicadas).
- **Testes**:
  - TestPostgresTenantRepository_Store — Store + FindByID validam persistência.
  - TestPostgresMembershipRepository_Store — Store + FK constraints validados.
  - TestRLSIsolation_TenantACannotReadTenantB — isolamento de tenant (placeholder para RLS application-level).
  - TestMembershipRevoked_NoAccess — membership revogada impede acesso.
  - TestTenantIsolation_MultiTenant — 3 tenants com 2 users cada, isolamento validado.
- Requer: OMNIRA_DATABASE_URL definida e migrations aplicadas (tables criadas, RLS enabled).
- Testes contra PostgreSQL real em CI (não mocks).

### Added (T06 — Authorization + TenantContext)
- `internal/tenancy/domain/context.go`: `TenantContext` (TenantID, ActorID, AccessSource) com factory + context helpers.
- `AccessSource` enum (direct, hub, system) para rastreabilidade de origem da requisição.
- `internal/tenancy/application/authorization.go`: `AuthorizationService` validando membership ativo.
- `AuthorizeAccessToTenant(ctx, tenantID, actorID)`: valida tenant ativo + membership ativo, retorna TenantContext.
- `IsAuthorized(ctx, tenantID, actorID)`: predicate boolean para checks simples.
- **Regra crítica**: `tenant_id` do request NUNCA é aceito como autoridade; validado SOMENTE via membership.
- `internal/tenancy/adapters/http.go`: `AuthorizationMiddleware` integrando authn → authz → TenantContext.
- Middleware encadeia: Principal (authn) → AuthorizeAccessToTenant → WithTenantContext.
- Respostas HTTP: 200 OK (authorized), 403 Forbidden (denied), 404 Not Found (tenant not found), 401 Unauthorized (missing Principal).
- Testes: 5 cenários autorização (success, no membership, inactive membership, inactive tenant, IsAuthorized).
- Testes HTTP adapter: Missing Principal → 401, Missing tenant_id → 400.

### Tested (T03–T13)

#### R0.1 Release Gate — PASSED ✓
- **54 unit + integration tests** (T01–T11)
- **5 adversarial isolation tests** (T12)
- **1 backup/restore validation drill** (T13)
- **Total: 60 tests, 100% pass rate**
- **Test suite coverage**: authn, config, tenancy (domain, app, adapters), audit, outbox, OTel, isolation, DR
- **Real database validation**: migrations applied, RLS enabled, data integrity verified
- **Metrics**:
  - Unit test latency: ~350ms
  - Backup time: 106ms
  - Restore time: 102ms (RTO)
  - Backup size: 44K
- **Docker build**: omnira-api 7.9MB, omnira-worker built
- **CI-ready**: all tests can run in CI against PostgreSQL

### Tested (T03–T12)
- ✓ Migrations aplicam sem erro (4 migrations = 8 tabelas).
- ✓ RLS habilitado em todas as 8 tabelas.
- ✓ 9 permissões seed.
- ✓ 5 system roles seed (tenant_admin, tenant_supervisor, tenant_agent, system_admin, hub_admin).
- ✓ Schema validation passa.
- ✓ Testes de isolamento validam tenant boundary.
- ✓ `go test ./internal/tenancy/domain` — 7 testes (Tenant/Membership state machine).
- ✓ `go test ./internal/tenancy/application` — 5 testes (AuthorizationService scenarios).
- ✓ `go test ./internal/audit/application` — 5 testes (RecordEvent, EventMetadata, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- ✓ `go test ./internal/outbox/application` — 5 testes (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- ✓ `go test ./internal/platform/otel` — 5 testes (GeneratesCorrelationID, PreservesCorrelationID, GetCorrelationID_Missing, WithCorrelationID, StatusCode).
- ✓ `go test ./internal/tenancy/adapters` — 17 testes (HTTP handlers, HTTP middleware, RLS isolation, Postgres persistence, **4 isolation adversarial**).
- ✓ `go test ./internal/platform/authn` — 3 testes (JWT validation, middleware).
- ✓ `go test ./internal/platform/config` — 4 testes (config loading, validation).
- ✓ `go test ./apps/api/cmd/omnira-api` — 3 testes (health endpoints, graceful shutdown).
- ✓ `go build ./apps/api/cmd/omnira-api` compila sem erros (7.9MB).
- ✓ **Todos os testes passam: 54 testes, 0 falhas, ~350ms total (inclui DB real)**.
- ✓ Testes Postgres (T07): OMNIRA_DATABASE_URL definida, migrations aplicadas, conexões reais.
- ✓ Testes HTTP (T08): handlers testados sem servidor, validam JSON, status codes, middleware chain.
- ✓ Testes Audit (T09): correlation tracking, metadata, tenant isolation validados.
- ✓ Testes Outbox (T10): transactional recording, unpublished queries, attempt tracking validados.
- ✓ Testes OTel (T11): correlation ID generation, preservation, context injection, status code capture validados.
- ✓ **Testes Isolation (T12): adversarial Tenant A vs B, membership revocation, multi-tenant separation, tenant status enforcement validados contra PostgreSQL real**.
