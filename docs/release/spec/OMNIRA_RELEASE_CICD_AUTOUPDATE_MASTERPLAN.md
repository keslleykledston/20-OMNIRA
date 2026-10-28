# OMNIRA — RELEASE ENGINEERING, CI/CD, GESTÃO DE VERSÕES E AUTO-UPDATE

**Plano Master + contrato de construção autônoma para Claude Code**  
**Versão da especificação:** 1.0 — 20/10/2026  
**Escopo:** repositório principal `keslleykledston/20-OMNIRA`, plataforma OMNIRA + Hub BPO + integrações/headless.  
**Natureza:** plano de implementação. **Este documento não significa que os componentes já foram desenvolvidos ou implantados.**

---

## 0. Resumo executivo e decisões fixas

Implementar uma cadeia de entrega completa:

```
Branches de feature (Claude/Codex/Lovable)
  -> PR e CI não privilegiado (Go / React / PostgreSQL real / RLS / contratos)
  -> merge controlado na baseline autorizada
  -> build imutável / SBOM / scan / provenance / assinatura
  -> homologação (staging) + testes de upgrade e recuperação
  -> release candidata -> release aprovada (canal stable)
  -> catálogo de releases assinado
  -> painel de administração global do OMNIRA
  -> pedido de atualização autenticado e auditado
  -> Update Agent isolado -> preflight -> backup verificável -> migração compatível
  -> deploy por digest -> readiness/smoke -> commit da atualização
  -> monitoramento e rollback de workloads, quando seguro
```

### Regra estrutural

O OMNIRA é **monólito modular Go + worker + frontend React/Vite e serviços de infraestrutura**, não um conjunto de microserviços por domínio. **Módulo funcional não significa pacote instalável independente.** Um módulo dentro do mesmo binário Go ou bundle Vite é versionado funcionalmente, mas atualizado por uma **release coordenada** do workload que o contém. Apenas workloads/plugins/adapters efetivamente separáveis e testados podem ser instalados isoladamente. Não criar microserviços ou plugin runtime apenas para simular atualização individual.

### Ambientes e autoridade

- **Feature/PR:** testado isoladamente; nunca atualizado em produção.
- **Development/lab:** pode publicar snapshots/prévias com TTL, sem secrets de produção.
- **Staging:** implantação automática *somente de artefatos candidatos aprovados pelos gates*.
- **Production/stable:** disponibilizar versão homologada no painel. Instalação apenas após ação expressa de **administrador da plataforma/operador de infraestrutura autorizado**. Administrador de tenant e operador/Hub BPO não têm autoridade para atualizar toda a instalação SaaS.
- **Uma instalação SaaS compartilhada:** uma atualização afeta todos os tenants no runtime; não instalar versões diferentes por tenant no mesmo binário.
- **Instalações dedicadas (quando houver):** cada `installation_id` tem estado, canal, política e atualização própria. Não presumir existência de múltiplas instalações.

**Sem intervenção frequente** significa que Claude Code implementa, testa, documenta e corrige autonomamente. **Não significa** aceitar alterações irreversíveis, inventar credenciais, ignorar gates ou efetuar um deploy real em produção sem autorização.

---

## 1. Evidências preliminares verificadas no repositório

Foi consultada a branch principal do GitHub em 20/10/2026; o agente deve repetir a auditoria local porque o repositório evolui paralelamente.

1. `CLAUDE.md`, `AGENTS.md`, `START-HERE.md`, `CONTEXT.md`, `CODEX.md` definem restrições de arquitetura, uso de agentes e segurança.
2. `go.mod` define módulo `github.com/omnira/omnira` e Go; Dockerfiles separados de API e worker já aceitam `VERSION`, `COMMIT`, `BUILD_TIME`.
3. `web/package.json` demonstra frontend **React + Vite + Vitest + Playwright**. Parte da documentação-base ainda cita **Next.js**. **Não converter stack**: manter implementação real e registrar ADR/ajuste documental específico se necessário.
4. Existem `docker-compose.yml`, `docker-compose.prod.yml`, Helm e docs de deploy. O `docker-compose.prod.yml` examinado declara explicitamente uso de **laboratório**, `OMNIRA_ENV=lab`, auth mock e Vite dev server. Não usar como template seguro de produção sem revisão.
5. `tools/migrate-sql.sh` já mantém `schema_migrations`, executa SQL pendente em transação por migração e possui controles de contexto de sessão. É o ponto de integração inicial a avaliar. O `Makefile` também contém comandos antigos de migração; **não utilizar `make migrate-down` como rollback automático**, nem scripts que reapliquem integralmente migrations.
6. O diretório `.github/workflows` não foi localizado na branch principal no momento da inspeção.
7. `START-HERE.md` pede leitura de documentos, testes incrementais, Docker-first, proteção da RLS e proíbe trocar arquitetura silenciosamente. Algumas referências de documentos podem estar ausentes no HEAD; localizar variantes, registrar ausência e não recriar sem contexto.
8. `MANIFEST.json` existente é índice de arquivos/checksums de pacote documental, **não** supor que seja o futuro Release Manifest. Não sobrescrever sem análise.

**Obrigatório:** reconciliar esses achados com o checkout local, migrations e deploy atuais antes de codificar. A constatação remota não equivale a auditoria operacional.

---

## 2. Arquitetura alvo e limites

### 2.1 Componentes

| Componente | Responsabilidade | Limite de segurança |
|---|---|---|
| CI Quality Gates (GitHub Actions) | lint, build, testes, banco real, contratos, isolamento, scans | PR sem secrets de deploy; privilégios mínimos |
| Release Builder | imagens imutáveis, metadados, SBOM, proveniência e assinatura | somente referências aprovadas/commit SHA, pipeline confiável |
| Release Registry | GHCR/OCI e índice estático de releases assinadas | não executar qualquer conteúdo indicado por metadados remotos |
| Release Catalog Service | consultar/verificar manifestos, dependências, compatibilidade, canais | sem docker socket, sem credenciais de administrador no frontend |
| Update Manager API | planejamento, RBAC, autorização, status, histórico e auditoria | administração **da plataforma**; sem autorização via `tenant_id` |
| Update Agent | downloader, verificador, backup, deploy, health, recuperação | fora do processo da API; invocação autenticada, allowlist rígida |
| Backup/Recovery Adapter | backup verificável de PG e mídias, restore de laboratório, runbooks | credenciais segregadas; backup externo; não apagar bancos automaticamente |
| Release Dashboard | lista de versões, diffs, mudanças, aprovações e logs resumidos | proteção frontend + autorização no servidor |
| Observability | eventos, métricas, traces e alertas | sem secrets, PII, tokens ou dumps em logs |

### 2.2 Topologia

```
GitHub PR ──> CI (não privilegiado) ──> merge protegido
                                            │
                                            v
                                 Release Builder confiável
                                            │
                                 OCI digest + attestations
                                            │
                                         GHCR
                                            │
                                  Signed Release Catalog
                                            │
                              OMNIRA Update Manager API
                                            │ pedido assinado/
                                            │ autenticado
                                            v
                              OMNIRA Update Agent (isolado)
                                    |            |
                              backups/PG      Docker deploy
                                    |            |
                                    └─ checks ──┘
```

**Deploy inicial:** preferir Docker Compose já adotado no projeto. Não impor Kubernetes, ArgoCD, Temporal, Kafka ou outra plataforma sem ADR de necessidade demonstrada. Deploy Adapter com interface para evoluir depois sem reescrever a regra de release.

**Agente independente:** processo/serviço no host administrado por systemd ou supervisor equivalente, com controle operacional restrito e diretórios de estado persistentes separados dos contêineres que ele atualiza. Se o host não permitir isso, fornecer implementação equivalente com isolamento e prova de sobrevivência ao restart da API. O acesso ao Docker é equivalente a privilégio de root: restringir host, usuário, comandos e canal de controle; **nunca** expor docker socket à API, ao navegador ou a workloads comuns. Não instalar um daemon que aceite shell remoto arbitrário.

**Plano de emergência:** quando o OMNIRA estiver fora do ar, operador deve conseguir ler status e recuperar versão anterior via CLI local segura, sem depender do frontend/PG para esse controle. O estado durável do Update Agent deve resistir à indisponibilidade da API e do banco (ex.: SQLite/WAL local protegido + reconciliação com auditoria central, ou mecanismo equivalente auditado).

---

## 3. Contrato de módulos, workloads e versões

Criar **inventário declarativo** do repositório (`release/components.yaml`, nomenclatura adaptável) com estas dimensões:

- `module_id`: módulo de domínio — ex.: `workspace`, `hub`, `flow_builder`, `erp_adapter`, `headless_api`.
- `build_unit`: pacote compilável real — ex.: `omnira-api`, `omnira-worker`, `omnira-web`.
- `deploy_unit`: container/workload real — ex.: `api`, `worker`, `web`.
- `migration_scope`: migrations compartilhadas `postgres/schema_migrations`, ou outra fonte existente (não criar migrations em paralelo).
- `api_contracts` e `event_contracts`: OpenAPI, AsyncAPI, webhooks, contratos de integração.
- `feature_flags` quando existem de fato.
- `dependencies`, `compatibility`, `owner`, `rollback_class`, `data_impact`.

**Regra:** módulo lógico pode apontar para um workload compartilhado; atualizar o módulo dispara atualização do workload e seus pares necessários. UI deve explicar **o que será atualizado efetivamente**. Não inventar hot swap de um submódulo Go embutido no mesmo binário.

### Versionamento

- Release de plataforma: SemVer `MAJOR.MINOR.PATCH`, exemplo ilustrativo `2.4.0`.
- Build id único: commit SHA completo + GitHub run ID + data UTC; build metadata quando aplicável.
- Imagens sempre fixadas por **OCI digest (`sha256:...`)**, nunca confiar em tag móvel como referência de deploy.
- Versão funcional dos módulos: identificador/estado no inventário, não afirma que o módulo é instalável isoladamente.
- DB schema: conjunto ordenado de migrations aplicadas, checksum por arquivo, compatibilidade declarada; evolução sem assumir que esquemas tenham SemVer de produto.
- Canais: `dev`, `candidate`, `stable` (adicionar `beta` só se justificar); canal não substitui a validade criptográfica da release.
- Promoção entre ambientes **reutiliza os mesmos digests**; não recompilar para produção.

### Exemplo de Release Manifest (ilustrativo)

```json
{
  "schema_version": 1,
  "release_id": "omnira-2.4.0",
  "platform_version": "2.4.0",
  "channel": "stable",
  "source": {
    "repository": "keslleykledston/20-OMNIRA",
    "commit_sha": "<full-git-sha>",
    "workflow_run_id": "<ci-run-id>"
  },
  "artifacts": [
    {"unit": "omnira-api", "image": "ghcr.io/<owner>/omnira-api@sha256:<digest>"},
    {"unit": "omnira-worker", "image": "ghcr.io/<owner>/omnira-worker@sha256:<digest>"},
    {"unit": "omnira-web", "image": "ghcr.io/<owner>/omnira-web@sha256:<digest>"}
  ],
  "modules_changed": ["workspace", "erp_adapter"],
  "compatibility": {
    "from_versions": ["2.3.x"],
    "min_agent_protocol": 1,
    "database_strategy": "expand_then_contract_later"
  },
  "migrations": {
    "start_after": "<last-known-applied-migration>",
    "target": "<last-release-migration>",
    "checksum_bundle": "sha256:<digest>"
  },
  "rollout": {
    "workloads": ["omnira-api", "omnira-worker", "omnira-web"],
    "preflight_policy": "strict",
    "recovery_class": "code_rollback_if_db_backward_compatible"
  },
  "release_notes_ref": "<immutable-referenced-document>",
  "approved_by": "<trusted-release-policy-id>"
}
```

Este JSON é um **modelo de campos**, não conteúdo pronto para executar. Definir schema JSON formal, canonicalização, versão de contrato, regras de desconhecidos, política de assinatura/atestado e validação de ponteiros seguros. Assinatura separada ou envelope verificável deve cobrir o conteúdo exato e a identidade do emissor. Rejeitar referências externas arbitrárias, imagens fora da allowlist e manifestos alterados. Exigir digest + origem + identidade da workflow e da referência Git autorizada; tags sozinhas nunca bastam.

### Compatibilidade

- Matriz release anterior → candidata (código, banco, API, worker, web, eventos, integração ERP, Hub e Headless).
- Migrations `expand` antes de migrar workloads; `contract` somente em release posterior, após ausência comprovada de leitores/escritores antigos, retenção e backup.
- Quando duas versões precisam coexistir, eventos e contratos devem suportar leitura por versão anterior durante rollout.
- Dependência transitiva e janela de atualização são validadas pelo planner; incompatibilidade = BLOCKED.
- Se a base implantada não corresponde a nenhum caminho de upgrade testado, não permitir instalação.

---

## 4. CI/CD e proteção de desenvolvimento paralelo

### 4.1 Branch/PR

- Toda tarefa nova do pacote deve iniciar em `feat/release-update-engine` (ou nome equivalente livre); usar **`git worktree` separado** quando a workspace atual tiver alterações, evitando tocar outros agentes.
- Guardar baseline SHA, status inicial, branch, worktrees e alterações alheias. **Nunca** `git reset --hard`, `git clean -fd`, checkout destrutivo, rebase de branches de terceiros, cherry-pick não solicitado ou push forçado.
- Ninguém publica uma branch de feature diretamente como release stable. CI em PR valida mudanças sem promover produção.
- Merges conforme política existente do repositório; se não existir, configurar checks/PR como recomendação e automatizar tudo possível, sem assumir controle administrativo inexistente.
- Suporte a merges paralelos, SHA fixo de build, concorrência por environment/installation e jobs canceláveis somente em PR (nunca interromper backup/deploy já iniciado).

### 4.2 Workflows esperados

Criar `.github/workflows/` com nomes estáveis e documentação:

1. `ci-pr.yml` — lint/format, vet, unit Go race, testes web, TypeScript, lint, builds, diffs de schema/contrato, scans de secrets/SCA, verificação de migrations, testes RLS adversariais PostgreSQL real, Playwright smoke relevante.
2. `ci-main.yml` — testes integrados e matriz robusta para commit mergeado, baseline de SBOM/coverage.
3. `release-build.yml` — somente ref confiável e gates verdes: multi-stage OCI builds, digest, SBOM, attestation/provenance, assinatura e publicação GHCR; produzir release manifest e release notes imutáveis.
4. `release-staging.yml` — promover **mesmos digests** a staging, aplicar migrações em cópia adequada, e executar E2E/contratos/segurança/recuperação.
5. `release-promote.yml` — validar todas as evidências, política de approvals e assinatura do catálogo; publicar `stable` sem tocar diretamente no host de produção.
6. `security-scheduled.yml` — SCA/imagens e dependências periodicamente, relatórios não bloqueantes para branches, bloqueantes na promoção quando severidade/regras exigirem.
7. `release-dryrun.yml` — reconstruir em ambiente descartável, simular upgrade de versão anterior, injetar falhas e observar rollback.

Nomes exatos podem se adequar ao inventário do repositório; nenhum job deve ser apenas placeholder verde.

### 4.3 Gates obrigatórios

**G0** Git/worktree íntegro e baseline clara; **G1** builds/linters; **G2** testes Go/frontend; **G3** PostgreSQL 16 real com RLS e grants Hub; **G4** migrations checksums + `expand/contract` + compatibilidade N-1; **G5** contratos API/webhook/AsyncAPI, inclusive Headless CRM; **G6** testes E2E/staging; **G7** SCA, SBOM, scan de secrets e imagens; **G8** artefato e manifest assinados/atestado verificado; **G9** teste de restore e rollback; **G10** consentimento/autorização de produção. Falhas obrigatórias bloqueiam promoção; proibir bypass silencioso.

**Hardening de Actions:** `permissions` explícitas mínimas por job, token de PR read-only, zero secrets em PRs não confiáveis, não rodar código não confiável com `pull_request_target` privilegiado, actions pinadas por SHA ou política equivalente, dependências imutáveis, cache confiável separado, `concurrency` e `environment` apropriados, registrando digest e run ID. Usar OIDC quando possível; não commitar credenciais. Atestados via GitHub são adequados quando plano/repositório oferecer suporte (no instante da pesquisa, GitHub Docs documenta restrições de disponibilidade para repositórios privados); quando indisponível, implementar cadeia de assinatura/proveniência equivalente com verificação estrita e sem reduzir o gate.

---

## 5. Release Catalog, distribuição e governança

- GHCR/OCI como registro inicial recomendado, condicionado a acesso real; release catalog assinado hospedado em endpoint autenticado/estático auditado. Não usar branch HEAD como catálogo de produção.
- Índice pequeno por canal com `latest_approved` e IDs; a atualização deve buscar e **verificar** manifest + proveniência + políticas antes de confiar em dados.
- Metadados: versão, autor da aprovação, SHA, build run, alterações, módulos, workload images, breaking changes, DB migration, janela, backup, classe de rollback, incidentes conhecidos, compatibilidade, security advisories.
- Retenção: manter artefatos e configurações de versões instaladas e da anterior durante janela de recuperação. Garbage collection nunca elimina digest referenciado por instalação ou rollback pendente.
- Promotion: candidate → staging tests → stable assinado. Revogação (`blocked/revoked`) com lista assinada e algoritmo anti-downgrade; rollback permitido só quando explicitamente compatível, sem burlar revogações críticas.
- Ambientes sem acesso ao registry: mostrar falha confiável e manter versão atual; não disparar atualização cega.

---

## 6. Update Manager: máquina de estados, APIs e dados

### 6.1 Máquina de estados

```
DISCOVERED → PLANNED → PREFLIGHTING → READY → AWAITING_APPROVAL
 → LOCKED → BACKING_UP → BACKUP_VERIFIED → DOWNLOADING → VERIFYING
 → PREPARING → MIGRATING_EXPAND → DEPLOYING → HEALTH_CHECKING
 → OBSERVING → SUCCEEDED

falha recuperável: FAILED → RECOVERING → ROLLED_BACK / RECOVERY_REQUIRED
impossibilidade de executar: BLOCKED
cancelamento permitido: antes da etapa de mutação; depois, somente via protocolo seguro
```

Definir transições legais e terminais em código, invariantes transacionais e persistência idempotente. Crash/restart do agente: reavaliar estado durável e **nunca repetir migração ou deploy de forma cega**. Deduplicar pedido com `idempotency_key`; TTL/heartbeat para leases e fencing tokens impedindo duas atualizações simultâneas. Lock por `installation_id` e backup/migration global; não depender só de lock em memória ou de PG caso PG esteja indisponível.

### 6.2 API sugerida (adaptar padrões existentes)

```
GET  /api/v1/platform/releases/current
GET  /api/v1/platform/releases/available
GET  /api/v1/platform/releases/{release_id}
POST /api/v1/platform/update-plans
GET  /api/v1/platform/update-plans/{plan_id}
POST /api/v1/platform/updates
GET  /api/v1/platform/updates
GET  /api/v1/platform/updates/{run_id}
GET  /api/v1/platform/updates/{run_id}/events
POST /api/v1/platform/updates/{run_id}/cancel
POST /api/v1/platform/updates/{run_id}/recover
GET  /api/v1/platform/update-system/health
```

- `/updates` necessita plano válido e não expirado, alvo por digest, idempotency key e evidência de aprovação. Recuperação não pode ser endpoint genérico com comando arbitrário.
- Streaming de progresso por SSE/WebSocket onde compatível; fallback polling; nunca confiar em estado de browser para status.
- Ações mutantes exigem RBAC de plataforma, CSRF/defesa de origem conforme sessão atual, reautenticação ou step-up para operações de alto impacto se mecanismo existir, rate limiting, auditoria append-only.
- Tenant admin, Hub membership, delegado do Hub e usuários do CRM externo não têm essa permissão; não confundir `EffectiveTenantContext` com autorização de infraestrutura.
- Eventos de auditoria: `actor`, instalação, solicitação, manifest digest, decisão do policy engine, estado anterior/novo, tempos, erro sanitizado, artefatos, backup verificado, motivo de rollback. Sem PII/segredos.
- Se o esquema de plataforma for compartilhado no PostgreSQL, desenhar o acesso a dados globais explicitamente e testar que tenants não o acessam. Preservar `FORCE ROW LEVEL SECURITY` nos dados tenant-owned; não usar `BYPASSRLS` da aplicação.

### 6.3 Modelo de persistência (conceitual, refinar após auditoria)

- `platform_installations`: instalação, ambiente, canal, versão aplicada, lock/estado, fingerprint da topologia.
- `platform_releases`: catálogo verificado/cache com digest e políticas (fonte assinada prevalece).
- `platform_update_plans`: pré-requisitos, diff de workloads, impactos, versão origem/alvo, expiração e policy hashes.
- `platform_update_runs`: execução, estados, idempotência, actor, release e correlação.
- `platform_update_events`: trilha append-only de progresso (sem segredo).
- `platform_backup_records`: evidência de backup, restore test, checksum, retenção, localização lógica protegida.
- `platform_component_versions`: unidades realmente instaladas por build/deploy unit; não declarar instalação independente de módulo embutido.
- Estado operacional durável independente do PG no agente, com sincronização reconciliável e resiliência a crash.

Para todas as tabelas: definir ownership, escopo, constraints, índices, migração, retenção, política de leitura/gravação e testes. Evitar duas fontes de verdade concorrentes: decisão de execução pertence ao agente/ledger durável e catálogo assinado; PG espelha histórico e view administrativa.

---

## 7. Preflight e execução controlada

### 7.1 Preflight (obrigatório e fail-closed)

1. Instalação identificada, status e ambiente corretos, identidade do administrador validada.
2. Agente e protocolo compatíveis; atualização do próprio agente segue procedimento separado com dupla versão/auto-recuperação.
3. Release assinada e não revogada; artifact digests disponíveis, permissões do registry e origem confiável.
4. Versão instalada real detectada via image digest/metadata e schema migration ledger, não apenas um campo manual.
5. Topologia do deploy inspecionada; workloads reais e volumes mapeados, sem confundir arquivos lab/dev com produção.
6. Migrations ordenadas e checksums íntegros; compatibilidade código/DB, ausência de arquivos reescritos ou migrations aplicadas desconhecidas.
7. Capacidade de host: CPU, memória, armazenamento livre (imagens + duas versões + backups + WAL), Docker daemon, rede, DNS/TLS.
8. Backups e recuperação: destino externo, criptografia, credenciais, espaço, retenção e prova recente de restore. **Backup de banco não equivale a backup de mídias, volumes NATS, configurações e chaves.**
9. Saúde de API, worker, frontend, webhooks, filas NATS/JetStream, entrega de mensagens e métricas; limites conhecidos e downtime estimado como hipótese, não promessa.
10. Concorrência: nenhum outro deploy, migration, backup incompatível, importação crítica ou recovery.
11. Modo de manutenção/drain e plano de retorno preparados; workers podem ficar em modo de pausa controlada; preservar reentrega/idempotência.
12. Chaves, segredos e configuração existentes jamais substituídos por defaults do exemplo `.env`.

Cada check: `PASS | WARN | BLOCK` + fundamento + recomendação. `BLOCK` não pode ser suprimido via UI sem mudar política e revalidar. `WARN` crítico requer autorização expressa de plataforma com trilha.

### 7.2 Deploy orquestrado

- Manter manifesto e backups/referências da versão anterior; arquivos compostos gerados de template testado com digests, não editar arquivos alheios em lugar não controlado.
- Baixar imagens antes do cutover, validar assinatura/attestation/digest e compatibilidade.
- Executar backup com identificação e checksum + restore de validação em ambiente descartável ou exigência de restore já comprovado conforme política da janela.
- Migrations `expand` estritamente via runner/ledger oficial, com travas, checksums e um executor só. Distinguir operações transacionais e não transacionais; não marcar como aplicada uma migration falha.
- Atualizar workloads em ordem segura determinada por dependências; readiness e smoke antes de trafegar chamadas, evitar atualização parcial prolongada quando contrato não suportar.
- Controlar NATS/JetStream e webhooks com drain e retry idempotente; não eliminar filas nem duplicar tickets/OS/ocorrências por efeito do deploy.
- Testar sessão/autenticação, RLS, Hub, Workspace, mídia, API externa/headless, webhook delivery, health, worker, mensagens e sinais de erro. Notificar sucesso somente quando runtime verificado e janela de observação concluída.
- Reconciliar estado do agente e banco; registrar versão instalada por digest, commit e migrations efetivamente aplicadas.
- Caso Compose single-host não possa garantir zero downtime, dizer claramente e usar janela controlada. Canary/blue-green só se topologia possuir balanceamento e coexistência verificada.

---

## 8. Recuperação, rollback e banco

**Proibição central:** não prometer rollback automático de dados. Não executar todas as `*.down.sql`; não rodar `make migrate-down` como mecanismo de recovery. Uma migração destrutiva não fica segura apenas porque existe `down.sql`.

Definir classes:

| Classe | Condições | Ação |
|---|---|---|
| `R0` | antes de qualquer mutação | abortar/limpar downloads, manter versão |
| `R1` | código novo falhou e esquema permanece compatível com versão anterior | reimplantar **digests antigos**, restaurar config compatível, checar health |
| `R2` | migração expand compatível + falha após upgrade | manter schema expandido e reimplantar código antigo testado com ele |
| `R3` | falha de dados, migration incompatível/destrutiva, corrupção ou não há compatibilidade | bloquear rollback automático; entrar em `RECOVERY_REQUIRED`, preservar evidências, usar runbook de restore/PITR com janela de perda de dados estimada e aprovação operacional |
| `R4` | release revogada/comprometida | isolar, consultar política de segurança e aplicar forward-fix/recovery autorizado; não reintroduzir vulnerabilidade por downgrade |

**Política de DB:** expand/contract, backups físicos e WAL/PITR quando ambiente suportar, backups lógicos quando adequados, restauração testada em instância descartável, RPO/RTO medidos, preservação de mídias externas e configurações, retenção e remoção segura de segredos. Em PostgreSQL, `pg_basebackup` é de cluster inteiro e PITR depende de WAL contínuo; `pg_dump` não substitui PITR. Sem evidência de restore e compatibilidade, gate bloqueia produção.

**Crash recovery:** persistir step antes/depois com evidência; restart do agente retoma consulta e reconciliação idempotente sem executar steps perigosos novamente. Corte de rede, disco cheio, falha de registry, falha do PG, falha do NATS, agent killed -9 e imagem corrompida devem ter tratamento documentado/testado.

**Recovery CLI:** consultar status e voltar *somente* workloads para digest anterior compatível; operação destrutiva de banco permanece manual e explicitamente aprovada; logs sanitizados. Sem acesso ao frontend.

---

## 9. Frontend de administração

Localização: **Administração global → Plataforma → Versões e Atualizações**. Não confundir com administração de tenants nem gestão do Hub.

Telas/estados:

1. **Resumo:** versão instalada, commit, canal, data de instalação, agente, health e status do catálogo.
2. **Disponíveis:** releases `stable` compatíveis, módulos alterados, changelog, breaking changes, segurança, DB, size/impacto, classe de rollback.
3. **Comparação:** atual vs candidata, workload e dependências, migrations, riscos, downtime esperado, blockers.
4. **Preflight:** cards de checks reais `PASS/WARN/BLOCK`, evidências sanitizadas e próximos passos.
5. **Confirmação:** versão/digest, escopo global, impacto em tenants/Hub, backup confirmado, usuário de plataforma, reautenticação se aplicável.
6. **Progresso:** etapas e tempos reais, logs/eventos sanitizados em streaming, retry controlado; navegação e refresh não perdem estado.
7. **Histórico:** versões, atores, timestamps, auditoria, falhas, rollback, links de evidências.
8. **Recovery:** orientações e botões disponíveis **apenas para operações comprovadamente seguras**.
9. **Preferências:** canal permitido, checagem periódica, janela de manutenção, alertas, retenção; instalar automaticamente em produção **desligado por padrão e fora do MVP**.

A UI segue código React/Vite do `web/`, padrões UX existentes e deve suportar erro/offline/sem atualizações/sem permissões/agent indisponível. Sem telas com dados falsos como se fossem reais; fixtures só nos testes.

---

## 10. Segurança e isolamento multi-tenant / Hub

- **Autorização global, não tenant:** acesso administrativo de plataforma separado de membership da instância, delegação Hub e clientes externos da API Headless.
- Todas as operações de tenant preservam `TenantContext`, grants Hub e RLS. O Update Agent não precisa receber dados de clientes para funcionar.
- PostgreSQL de runtime continua com `omnira_app`, nunca superuser/BYPASSRLS; migrations usam identidade privilegiada **separada e restrita** em execução auditada.
- Nunca expor `/internal/*`, endpoints de agent ou controles do Docker pelo Nginx público.
- Release catalog e pacotes são entradas não confiáveis até assinatura, digest e identity verificados.
- Sem remote shell, script de release arbitrário, caminhos de filesystem controlados por manifesto, concatenação de comando, secrets em outputs ou logs.
- CSRF/origin, RBAC/reautenticação, allowlist de instalação/digest/canal, prevenção replay, fencing lock, proteção SSRF e verificação TLS.
- Hardening da cadeia CI evita ataques de PR de fork, dependências maliciosas, poison cache, assinatura por workflow não confiável.
- Fazer testes de abuso: tenant tentando acessar endpoints, Hub delegado, API Headless, token expirado, assinatura adulterada, versão revogada, dupla aprovação e concorrência.

---

## 11. Observabilidade e operação

Métricas sugeridas: `release_check_success`, `update_runs_total{status}`, `update_duration_seconds`, `update_stage_duration_seconds`, `rollback_total`, `agent_heartbeat_age`, `backup_last_verified_age`, `migration_drift_detected`, `health_post_deploy_failures`, `registry_fetch_failures`. Aplicar cardinalidade controlada e evitar labels com PII/IDs arbitrários.

Alertas: agente inacessível, falha de backup/restore, atualização travada/lock expirado, rollback incompleto, divergência entre versão real e catálogo, aumento sustentado de erros em API/worker/webhooks, NATS pending/lag elevado, storage crítico.

Manuais: instalação do agente, primeira configuração de registry/signing, recuperação offline, rotação de credenciais, backup/restore, revogação de release, upgrade de agente, troubleshooting, migrações incompatíveis, atualização manual `dry-run` e contingência.

---

## 12. Fases de entrega e gates por fase

**F0 — Descoberta read-only e baseline (sem modificações).**
Inventariar árvore, branches/worktrees, dirty status, CI, Docker, frontend, Go, migrations, Nginx, auth, envs, agentes, docs, DB, backup, observability. Entregar `docs/release/00-audit.md` com inconsistências e decisões, matriz de risco, escopo real. Gate: sem rollback/limpeza/alteração em trabalho alheio.

**F1 — ADR, contratos e testes de especificação.**
Definir vocabulário, topology/deploy adapter, release/component schemas, state machine, RBAC, recoverability classes, threat model. Criar ADRs necessários, docs e testes do parser/schema. Gate: preservar monólito modular, isolamento e versionamento real.

**F2 — CI Quality Gates no GitHub.**
Workflows com Go, Vite, PostgreSQL 16, RLS cross-tenant/Hub, contratos, migration lint, SCA, security, browser e smoke. Scripts locais determinísticos replicam CI. Gate: pipeline real não placebo, PR sem secrets privilegiados.

**F3 — Release build e assinatura.**
Construção por commit aprovado, GHCR digests, SBOM, attestation/signature, build info no runtime, release manifest validado; promoção e catálogos de teste. Gate: package tampering rejeitado, rebuild não obrigatório entre ambientes.

**F4 — Banco, backups e compatibilidade.**
Integrar `tools/migrate-sql.sh` ou migrator canônico testado; adicionar ledger/checksum/detecção drift sem reescrever migrations aplicadas; backups PG e dados externos, restauração descartável, teste de upgrade N-1. Gate: migrations incompatíveis bloqueadas, restore verificado.

**F5 — Update Agent e CLI independente.**
Ledger durável, assinatura/verificação, locks, fencing, preflight, download, deploy adapter Docker Compose, health checks, recovery CLI, reconciliador. Gate: matar API e agente no meio sem corromper ou repetir operação perigosa.

**F6 — Backend API e autorização de plataforma.**
Service/use cases, rotas OpenAPI, auditoria, streams, run records, idempotency, RBAC, integrações com Update Agent; segurança/tenancy. Gate: usuários tenant/Hub/headless negados, administrador autorizado validado.

**F7 — UI e fluxo de confirmação.**
React/Vite real, resumo, releases, diff, preflight, aprovação, acompanhamento, histórico e recovery. Gate: Playwright básico, acessibilidade/responsividade e estados offline.

**F8 — Deploy de staging e testes de falha.**
Subir ambiente efêmero/lab e staging se credenciais existentes, fazer upgrade de release anterior, backups, fault injection, rollback código, observação de fila/webhooks, medição RPO/RTO. Gate: relatórios com provas e limites.

**F9 — Catálogo stable, hardening e compatibilidade global.**
Revisar assinatura, supply chain, revogação, canais, telemetria, retenção, migration safety, atualização agente, multi-installation se aplicável. Gate: Codex independente + security review sem CRITICAL/HIGH bloqueante.

**F10 — Operação assistida não destrutiva e documentação.**
Runbooks, `Makefile` targets novos seguros, dev env, health, troubleshooting, guia admin, OpenAPI, ADRs, testes e changelog. Gate: operador simula e recupera cenário completo em laboratório.

**F11 — Entrega de código pronto, *não* deploy live presumido.**
Branch, commits por fase, PR se acesso disponível e política permitir, evidence pack, matriz PASS/FAIL/BLOCKED, instrução de configurar secrets/permissões/infra ainda faltantes. **Não ativar produção sem aprovação explícita da instalação.** Gate: sem alegação de E2E/rollback de produção sem prova.

### Gates adicionais que devem estar presentes em toda fase

- Testes repetíveis localmente e em CI; documentar comandos, logs resumidos, SHA, tempo e ambiente.
- Codex ou subagente independente revisa diff e procura violações: leak secrets, RLS, replay/idempotência, migrations, crash recovery, desenho de permissões e rollback.
- Qualquer bug encontrado deve ser corrigido e revalidado no mesmo ciclo, até passar ou classificar BLOCKED com evidências.
- Atualizar contrato/API, docs de handoff, CHANGELOG e `.changes/` para alterações visíveis.
- Não mascarar checks com `|| true`, `skip`, mock onde exigida integração real; se recurso externo não existe, CI local rodável e integração externa BLOCKED — não PASS.

---

## 13. Matriz de testes mínimos (aceitação)

| Cenário | Evidência esperada |
|---|---|
| PR malicioso tenta executar deploy | PR não recebe segredos nem ambiente de publicação |
| Commit muda componente Go compartilhado | Planner promove release coordenada do workload, não hot-swap de módulo |
| Modificação em `*.up.sql` já aplicada | mismatch de checksum detectado e instalação bloqueada |
| Migrations rodam duas vezes por retry | uma aplicação válida no ledger, idempotente |
| Dois administradores clicam atualizar simultaneamente | uma execução, outra recusada/associada via idempotência |
| Novo digest sem assinatura válida | pacote rejeitado, sem deploy |
| Registry fora do ar | versão atual permanece; erro explicativo |
| API indisponível no upgrade | agente independente mantém/recupera controle |
| Agent sofre crash e reinicia | reconcilia estado sem deploy/migração duplicados |
| Readiness da API nova falha e DB compatível | rollback para digest anterior + health comprovado |
| DB migração destrutiva/incompatível | upgrade bloqueado antes da mutação |
| Restore teste falha | upgrade stable bloqueado |
| NATS com mensagens pendentes | sem perda silenciosa; retry/idempotência validada |
| Tenant/Hub tenta atualizar runtime global | HTTP 403/404 apropriado + auditoria, nenhuma execução |
| Hub grant revogado / cross-tenant | RLS e acesso negado conforme contrato existente |
| API Headless CRM recebe evento durante upgrade | sem quebra de contrato nem duplicação não idempotente |
| Frontend recarrega após iniciar atualização | estado e timeline retomados do servidor |
| Tenta downgrade incompatível | política bloqueia e informa razão |
| Host de laboratório usa arquivo `prod.yml` | detecção de ambiente pelos valores/identidade reais, não pelo nome do arquivo |

---

## 14. Entregáveis verificáveis e caminhos sugeridos

```
.github/workflows/{ci-pr,ci-main,release-build,release-staging,release-promote,release-dryrun,security-scheduled}.yml
release/components.yaml
release/schemas/{release-manifest,component-inventory}.schema.json
release/policies/{channels,compatibility,signing}.yaml
internal/platform/release/*            # usar estrutura que já existir, em vez de duplicar
internal/platform/update/*             # machine state, policies, orchestration
apps/update-agent/*                    # ou local adequado sem novo monólito grande
apps/updatectl/*                       # CLI de emergência, se necessário
web/src/...                            # rotas/componentes conforme app real
migrations/*.up.sql                    # só additive, sob runner canônico
scripts/release/*                      # scripts idempotentes e testáveis
docs/release/{00-audit,architecture,runbooks,disaster-recovery,security,operator-guide}.md
docs/adr/*                               # mudanças arquiteturais necessárias
contracts/openapi/*                     # integrar contratos preexistentes
```

**Importante:** caminhos são **propostos**, não instrução de criar duplicatas. Claude deve mapear a árvore real e preferir estrutura canônica existente.

---

## 15. Convenção de autonomia e limites objetivos

**O agente decide sozinho** nomes de funções, layout de arquivos, frameworks de teste compatíveis, convenções de lint, detalhes reversíveis, filtros de CI, mensagens de UI e prioridades que não mudem contratos ou segurança; registra escolhas relevantes.

**O agente investiga e propõe ADR antes de avançar** quando confrontar conflito entre implementação e decisões congeladas, incompatibilidade de schema/tenancy, dependência em produção, mudança de domínio, fluxo de autenticação ou política operacional; pode prosseguir com outras fatias independentes.

**Perguntar somente se ambiguidade verdadeira** impede decisão tecnicamente segura: qual instalação real é produção quando há duas candidatas indistinguíveis, qual política comercial de canais aplicar quando nenhum documento define e a escolha cria consequência irreversível, autorização expressa exigida para manipular dados reais, ou dois ADRs Accepted incompatíveis. Pergunta objetiva, com opções, evidências e decisão recomendada. Não perguntar por detalhes técnicos solucionáveis por auditoria.

**Bloquear e documentar — não adivinhar:** credenciais GHCR/host, permissões de settings e GitHub Environments, DNS, backups externos ou acesso produção indisponíveis. Implementar tudo testável com fixture local e documentar o passo de conexão. Nunca gerar segredo fake e alegar integração real. Sem merge direto em branch protegida nem deploy produção sem pré-autorização existente.

**Relatório final de Claude Code:**

1. branch/worktree e baseline SHA; commits e PR;
2. fases F0–F11 (`PASS / FAIL / BLOCKED / NOT_APPLICABLE`) e evidência;
3. arquivos e migrations; novos endpoints/contratos;
4. workflows e releases/digests realmente gerados;
5. comandos de testes executados, ambiente e outputs relevantes;
6. verificação independente Codex, achados e correções;
7. segurança e RLS; riscos e limitações;
8. operações de produção **não executadas**; pré-requisitos externos se houver;
9. runbooks/rollback comprovados no laboratório;
10. recomendações sequenciais com próximos passos sem pendências invisíveis.

---

## 16. Referências oficiais técnicas para o implementador

- GitHub Actions — artifact attestation e provenance: https://docs.github.com/en/actions/how-tos/secure-your-work/use-artifact-attestations/use-artifact-attestations
- GitHub Actions — riscos de `pull_request_target`: https://docs.github.com/en/actions/reference/security/securely-using-pull_request_target
- PostgreSQL 16 — backups físicos (`pg_basebackup`): https://www.postgresql.org/docs/16/app-pgbasebackup.html
- PostgreSQL 16 — WAL archiving / PITR: https://www.postgresql.org/docs/16/continuous-archiving.html
- Docker Compose: https://docs.docker.com/compose/
- Sigstore Cosign: https://docs.sigstore.dev/cosign/overview/

> A disponibilidade de funcionalidades externas, versões de actions e capacidades do ambiente deve ser verificada no dia da execução. Não reproduzir tokens ou comandos de documentação sem revisão do contrato operacional.

---

## 17. PROMPT MASTER — COPIAR INTEGRALMENTE PARA CLAUDE CODE

> **A instrução completa de execução autônoma está também em arquivo separado** `OMNIRA_CLAUDE_CODE_PROMPT_RELEASE_ENGINEERING.md`. Usar esse arquivo como prompt inicial para evitar cortes de contexto e manter este plano como especificação normativa.

**Objetivo do prompt:** implantar no repositório o sistema completo descrito neste documento, executando F0–F11 sem intervenções rotineiras, conservando mudanças paralelas e bloqueando apenas ações inseguras.

**Diretriz de precedência para implementação:** ADR Accepted + instruções canônicas atuais do repositório > contrato deste plano onde não contraditório > escolhas locais documentadas. Havendo conflito material, registrar e resolver com estratégia conservadora, criando ADR em mudança proposta; não apagar fatos do repositório em nome do plano.

**Controle final:** nenhuma funcionalidade marcada concluída sem prova observável: workflow válido, testes, auditoria, logs, health, rollback ou evidência de ambiente externo bloqueado.
