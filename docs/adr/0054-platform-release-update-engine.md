# ADR-0054 — Platform Release/Update Engine: boundaries, trust model, deployment model

Status: **Aceito (2026-10-20).** Nenhuma ambiguidade material bloqueante foi encontrada na auditoria F0 nem na revisão desta fase: os boundaries propostos reaproveitam mecanismos já aceitos e implementados (ADR-0038, ADR-0041, ADR-0042, `tools/migrate-sql.sh`, padrão append-only), sem introduzir Kubernetes/Temporal/Kafka nem um segundo mecanismo de RBAC. Este documento guia F2–F11; `internal/platformrelease/domain` (manifesto + máquina de estados) e `release/{components.yaml,schemas/,policies/}` são sua primeira implementação (F1).
Relaciona: ADR-0038 (Hub/plataforma, `platform_operators`), ADR-0041 (console de administração independente), ADR-0042 (contexto de sessão assinado), `docs/architecture/MODULE-MANAGEMENT.md`, `docs/architecture/PLATFORM-BASELINE.md`, `docs/release/00-audit.md` (F0).

## 1. Contexto

O OMNIRA precisa de um caminho auditável e seguro para: (a) validar mudanças de código em CI real com Postgres/RLS; (b) construir artefatos imutáveis assinados; (c) publicar um catálogo de releases verificável; (d) permitir que um operador de plataforma autorizado (não um admin de tenant, não um admin de Hub) peça a atualização da instalação SaaS inteira; (e) executar essa atualização com backup verificável, preflight fail-closed e recuperação segura — sem nunca prometer rollback automático de dados.

A F0 (`docs/release/00-audit.md`) encontrou que boa parte da fundação já existe:
- RBAC de plataforma já existe (`platform_operators`, ADR-0038) — não é preciso inventar um novo mecanismo de autorização global, só reaproveitar.
- Já existe um console de administração separado do app do agente (ADR-0041, `web/src/admin/`) — a UI de versões/atualizações entra ali.
- Já existe um runner de migration idempotente e seguro (`tools/migrate-sql.sh`) e um perigoso e legado (`make migrate-up/down`) que a automação nunca deve chamar.
- Este host roda hoje em `LIMITED_INTERNAL_PRODUCTION` real (WhatsApp conectado de verdade) — qualquer staging/teste de falha deste programa precisa de isolamento comprovado, nunca tocar o stack ao vivo sem autorização explícita.
- Não há Kubernetes sancionado (há um chart Helm não aprovado, que este programa não usa).

## 2. Decisão

### 2.1 Limites do sistema (boundaries)

Novo módulo de domínio Go: **`internal/platformrelease/`**, seguindo o layout padrão do repositório (`domain/`, `application/`, `ports/`, `adapters/`), irmão de `internal/hub/` — **não** aninhado em `internal/platform/` (que é infraestrutura transversal: authn, db, messaging, telemetry, httpserver, não um domínio de negócio). Nome escolhido autonomamente (regra §15 da especificação: nomes reversíveis não exigem pergunta) para não colidir semanticamente nem com `internal/platform` nem com `internal/hub`.

Componentes e seus limites de confiança:

| Componente | Vive em | Confia em | Não confia em |
|---|---|---|---|
| CI Quality Gates | `.github/workflows/` | nada além do próprio runner | segredos de deploy em PR de fork |
| Release Builder | `.github/workflows/release-build.yml` + `scripts/release/` | SHA de ref confiável (branch protegida) | tags móveis, builds não reproduzíveis |
| Release Catalog | artefato estático assinado (GHCR + índice) | assinatura/digest | nome de arquivo, branch HEAD, metadados não assinados |
| Update Manager API | `internal/platformrelease/` + `/api/v1/platform/*` | `platform_operators` (ADR-0038), RLS | `tenant_id` de payload, membership de tenant/Hub, API Headless |
| Update Agent | `apps/update-agent/` — processo systemd **separado** do binário `omnira-api` | manifest assinado + digest verificado | shell remoto, docker socket exposto à API/web |
| Backup/Recovery Adapter | `scripts/release/` reaproveitando `scripts/backup-omnira-*.sh` existentes | credenciais segregadas, destino externo | apagar o banco vivo automaticamente |
| Recovery CLI | `apps/updatectl/` | estado durável local do agente | frontend/API (funciona mesmo com os dois fora do ar) |

### 2.2 Modelo de confiança e segurança

1. **Autorização é sempre `IsPlatformOperator`** (`internal/hub/provisioning/operators.go:83-91`), nunca `tenant_id`/membership/Hub/Headless. Endpoints `/api/v1/platform/*` seguem o padrão de `internal/hub/adapters/admin_http.go` (404 uniforme em negação, nunca 403, `Idempotency-Key` obrigatório em mutações).
2. **A API nunca executa shell ou Docker diretamente.** Ela só enfileira um pedido autorizado (tabela `platform_update_runs` + evento NATS `job.platform.update.apply.v1`, seguindo o padrão de consumidor já existente em `internal/worker/routing/consumer.go`); o Update Agent, processo separado, é quem executa.
3. **O Update Agent nunca expõe endpoint remoto de shell nem Docker socket público.** Acesso a Docker é equivalente a root — fica restrito ao host, ao usuário que roda o agente, e a um conjunto fixo de comandos (`docker compose pull/up/ps`), nunca comando arbitrário vindo do manifest.
4. **Assinatura/digest são verificados antes de qualquer download/execução.** Release manifest + imagens por digest OCI (`sha256:...`), nunca tag móvel. Assinatura inválida = bloqueio (fail-closed), sem exceção configurável via UI.
5. **Migrations usam exclusivamente `tools/migrate-sql.sh up`** (idempotente, ledger `schema_migrations`). O Update Agent nunca invoca `make migrate-up`/`make migrate-down`. Rollback de dados nunca é automático — ver classes de recuperação (§2.5).
6. **RLS nunca é relaxada por conveniência.** Tabelas novas de plataforma (`platform_installations`, `platform_releases`, `platform_update_plans`, `platform_update_runs`, `platform_update_events`, `platform_backup_records`, `platform_component_versions`) usam `FORCE ROW LEVEL SECURITY` com policy restringindo a `is_platform_operator()`, mesmo padrão de `platform_operators` (`migrations/000099_platform_operators.up.sql:18-25`). `platform_update_events` usa o padrão de append-only reforçado no banco (`migrations/000087_append_only_tables.up.sql`: `REVOKE UPDATE, DELETE` + trigger), não só disciplina de aplicação.
7. **Isolamento de ambiente ao vivo:** qualquer ambiente de staging/fault-injection (F8) roda com nome de projeto Docker, rede e volumes **próprios**, nunca reaproveitando os do stack principal (`omnira`) ou do headless (`omnira-hl`). O Update Agent nunca reinicia o container WAHA do stack principal fora de uma janela de deploy explicitamente autorizada (achado PILOT-RUNBOOK §6: reiniciar WAHA sem necessidade arrisca exigir novo QR de uma conta real).

### 2.3 Modelo de implantação

Docker Compose, conforme já decidido no `PLATFORM-BASELINE.md` e reforçado por `CLAUDE.md`/especificação: nenhuma introdução de Kubernetes/Temporal/Kafka. O chart Helm não sancionado (`helm/omnira/`, achado F0 §5.4) não é referência nem é usado. O Update Agent roda como processo/serviço supervisionado (systemd) no host, com estado operacional persistido fora do Postgres (ex.: SQLite local com WAL), sobrevivendo a indisponibilidade da API/banco — exigência explícita da especificação (§2.2 do Master Plan: "resistir à indisponibilidade da API e do banco").

### 2.4 Arquitetura de dados global

Tabelas novas no schema compartilhado do Postgres existente (não um banco separado — reaproveita a infraestrutura de RLS já comprovada), prefixo `platform_` para não colidir com nada de tenant:

- `platform_installations` — uma linha por instalação (hoje: uma só, esta SaaS compartilhada); canal, versão aplicada, lock/estado, fingerprint de topologia.
- `platform_releases` — cache local do catálogo assinado (fonte de verdade é o catálogo remoto assinado; esta tabela é só cache/índice de consulta).
- `platform_update_plans` — pré-requisitos, diff de workloads, expiração, hash de política.
- `platform_update_runs` — execução, estado (máquina de estados §2.5), idempotency key, actor.
- `platform_update_events` — trilha append-only de progresso (sem segredo), **REVOKE UPDATE/DELETE** reforçado.
- `platform_backup_records` — evidência de backup e teste de restore (liga-se aos scripts já existentes, não os substitui).
- `platform_component_versions` — unidades realmente instaladas por build/deploy unit (nunca "módulo instalado" para um módulo embutido no mesmo binário).

Autoridade de execução: o **ledger durável do Update Agent** (fora do Postgres) é quem decide o próximo passo seguro após um crash; o Postgres espelha histórico e alimenta a view administrativa — nunca duas fontes de verdade concorrentes para "o que fazer agora".

### 2.5 Máquina de estados e classes de recuperação

Reaproduzidas exatamente como a especificação normativa descreve (`docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md` §6.1, §8) — este ADR não as redefine, apenas as adota como contrato. Resumo das classes de rollback: `R0` (antes de mutar, aborta limpo) · `R1` (código novo falhou, schema ainda compatível → reimplanta digest antigo) · `R2` (migração expand compatível + falha pós-upgrade → mantém schema, reimplanta código antigo) · `R3` (dado incompatível/corrompido → `RECOVERY_REQUIRED`, nunca automático) · `R4` (release revogada → isola, não faz downgrade cego).

### 2.6 Módulos e versionamento (`release/components.yaml`)

Usa o vocabulário já definido em `docs/architecture/MODULE-MANAGEMENT.md` (identity, tenancy, audit, contacts, conversations, tickets, routing, channels, integrations, tools, realtime, hub, sla, automation, analytics) como `module_id` — não inventa um vocabulário paralelo. `build_unit`/`deploy_unit` mapeiam para os três workloads reais hoje: `omnira-api`, `omnira-worker`, `omnira-web` (confirmado pelos `ARG VERSION/COMMIT/BUILD_TIME` já existentes em `Dockerfile.api`/`Dockerfile.worker`, prontos para builds versionados). Ver `release/components.yaml` nesta mesma worktree.

## 3. Consequências

**Positivas:** reaproveita quase toda a fundação de autorização/RLS/append-only/NATS/observabilidade já comprovada em produção; não duplica RBAC nem cria um segundo console; mantém o monólito modular Go; nenhum novo serviço de infraestrutura cara.

**Negativas/custos:** novo processo (Update Agent) a operar e supervisionar; nova tabela de ledger local fora do Postgres a manter; exige disciplina para nunca deixar `platformrelease` escrever fora de `IsPlatformOperator`; o host atual tem gaps operacionais reais (cron de backup caído — achado F0 §5.3) que precisam ser corrigidos **antes** de qualquer promoção além de `LIMITED_INTERNAL_PRODUCTION`.

## 4. Alternativas consideradas e rejeitadas

- **Novo papel de autorização do zero** (em vez de `platform_operators`): rejeitado — duplicaria um mecanismo já implementado, testado e com CLI de gestão segura (`omnira-hubctl platform-operator`).
- **Nova entrada de frontend separada** para versões/atualizações (terceiro shell Vite): rejeitado por ora — o console ADR-0041 já resolve exatamente esse tipo de necessidade ("gestão de plataforma separada do app do agente"); criar um quarto shell seria duplicação sem necessidade demonstrada (mesmo critério de "Extração de serviço" do `MODULE-MANAGEMENT.md` §Extração).
- **Kubernetes/Helm para o deploy do updater**: rejeitado — sem ADR aprovado, sem necessidade operacional demonstrada, contradiz `PLATFORM-BASELINE.md` e `CLAUDE.md`.
- **Update Agent como goroutine dentro do próprio `omnira-api`**: rejeitado — violaria a exigência de sobreviver a crash/indisponibilidade da própria API, e misturaria o privilégio equivalente-a-root de acesso Docker com o processo que recebe tráfego HTTP público.
