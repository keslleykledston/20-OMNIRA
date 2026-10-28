# OMNIRA — ARQUIVO ÚNICO PARA EXECUÇÃO PELO CLAUDE CODE

**Instrução de uso:** disponibilize este arquivo ao Claude Code no repositório OMNIRA e diga: “Leia integralmente este arquivo e execute a missão da primeira seção, usando o plano da segunda seção como especificação. Trabalhe autonomamente, preserve alterações paralelas e só me consulte perante ambiguidade essencial.”

O texto a seguir contém **(I) o prompt de implementação** e **(II) o Plano Master completo**. As referências ao arquivo separado de plano devem ser interpretadas também como referência à segunda seção deste mesmo documento. O Claude Code deve persistir os documentos na branch/worktree isolada que criar.

---

# PARTE I — PROMPT DE IMPLEMENTAÇÃO

# PROMPT DE EXECUÇÃO AUTÔNOMA — CLAUDE CODE

## OMNIRA — CICD / RELEASE MANAGER / AUTO-UPDATE / RECOVERY

> **Use este texto integralmente como instrução para Claude Code no checkout do repositório principal do OMNIRA.**
>
> **Anexo normativo:** `OMNIRA_RELEASE_CICD_AUTOUPDATE_MASTERPLAN.md`. Coloque ambos os arquivos em local acessível ao Claude Code (por exemplo em `docs/release/` na worktree dele ou via prompt/anexo), para que leia a especificação inteira e a mantenha como referência. O caminho definitivo no repo é escolha do agente.

---

Você é o engenheiro principal responsável por implementar de ponta a ponta o **OMNIRA Release Engineering System**, desde CI/CD do desenvolvimento paralelo até o Update Manager com frontend administrativo, backend, agente independente, backup, verificações, publicação de release, recuperação e rollback seguro.

**MISSÃO:** Construir **todo** o escopo técnico das fases F0–F11 do documento Master, em cortes verticais verificáveis, sem solicitar intervenções de rotina ao usuário. Não encerrar após produzir somente documentação, wireframes, YAMLs ou scaffolds. Escrever código executável, contratos reais, testes, scripts, workflows, documentação operacional, UI e ferramentas de recuperação. Integrar ao OMNIRA real, não criar demo separada.

## A. Modo operacional e autonomia

1. Antes de qualquer edição, ler `START-HERE.md`, `CONTEXT.md`, `CLAUDE.md`, `AGENTS.md`, `CODEX.md`, `docs/product/MVP.md` e documentos/ADRs atuais pertinentes. Verificar a existência dos caminhos e identificar documentos renomeados ou ausentes. Ler o **Master Plan** integralmente.
2. Auditar o repositório **somente leitura** antes de editar: `git status --short`, branch, SHA da baseline, `git worktree list`, branches ativas, diffs locais, migrations, estrutura Go, frontend, Docker, compose/Helm, Nginx, workflows, scripts, autenticação, RLS, contratos, observabilidade, acesso ao CI/GHCR, backups, mídia e topologia de deploy.
3. Isolar o trabalho em **branch nova** `feat/omnira-release-update-engine` ou nome único equivalente, preferindo `git worktree` dedicado. **Não** mexer em alterações locais/branches de outros agentes, não fazer reset/clean/stash alheio, rebase de terceiros ou push forçado. Preservar trabalhos paralelos intocados.
4. Use ferramentas disponíveis em seu ambiente, inclusive terminal, testes, Docker, PostgreSQL de laboratório, MCP do GitHub e subagentes. Use Codex via integração disponível para revisão independente **ao fim de cada fase crítica**, e novamente antes da entrega final. Se Codex não estiver disponível, marcar `INDEPENDENT_REVIEW_BLOCKED` e executar revisão adicional, sem afirmar que Codex validou.
5. **Execute autonomamente** o plano completo em sequência. Não pergunte preferências sobre nomes, bibliotecas, detalhes reversíveis de implementação, estrutura de arquivos, UI, comandos, testes ou divisão de subtarefas. Leia o código para decidir e documente suposições.
6. **Só formule pergunta** quando há ambiguidade genuína, material e insolúvel pelos contratos ou quando uma autorização/credencial real é necessária para ação perigosa ou irreversível. Seja objetivo: evidência, opções, recomendação e consequência. Em caso de bloqueio parcial, continue concluindo outras fases seguras e reporte `BLOCKED` claramente. Não invente acesso, segredos, aprovação ou deploy realizado.
7. Nunca execute ação destrutiva em produção, restaure banco real, remova volumes, reduza política de RLS, altere domínio público, crie infraestrutura cara, altere branch protegida ou publique stable sem os gates e a autorização apropriados. A **implementação** deve ficar funcional e verificável em lab/staging; a **instalação real em produção** é ação posterior de administrador autorizado no OMNIRA, não parte da execução sem consentimento.
8. Não refatore partes não relacionadas, não converta a aplicação em microserviços, não adicione Kubernetes, Temporal ou Kafka sem ADR formal aprovado; preserve monólito modular Go, PostgreSQL 16 com RLS, Hub BPO, CRM/headless, worker/NATS, Docker-first e UX atual.
9. Ao detectar conflitos entre docs e código, investigue fonte efetiva de verdade. Conflito conhecido: `START-HERE.md` menciona Next.js, mas `web/package.json` no GitHub consultado usa React/Vite. **Não migrar o frontend**; implementar na stack realmente executada, registrando a divergência e uma correção documental/ADR quando pertinente.
10. Conflito conhecido: `docker-compose.prod.yml` tem configuração de laboratório/mock; não inferir ambiente real pelo nome do arquivo nem tratá-lo como produção segura. `MANIFEST.json` existente não é necessariamente Release Manifest. `Makefile` contém comandos de migração perigosos para updater automático. Reconciliar com HEAD local antes de alterar qualquer coisa.
11. Trabalhe até que cada fase tenha aceitação demonstrável, ou impedimento externo explicitamente comprovado. Proibido disfarçar falhas com `|| true`, mocks em testes que requerem Postgres/containers reais, `TODO` crítico ou documentação com alegações falsas.

## B. Objetivo funcional definitivo

Construir o ciclo:

`feature branches -> PR CI -> merge protegido -> build imutável -> SBOM/proveniência/assinatura -> release candidate -> staging com E2E e ensaio de rollback -> promoção para stable -> catálogo verificável -> painel de administração global -> preflight -> autorização -> backup verificável -> deploy gerenciado -> health/smoke -> success ou recuperação`.

O sistema deve comportar módulos em desenvolvimento simultâneo, mas **não** empacotá-los individualmente quando compartilham binário/bundle. Defina inventário de `module_id`, `build_unit` e `deploy_unit`, com dependências transitivas e migrações. Publicação stable somente de artefatos integrados e aprovados. Não instalar branches de desenvolvimento no ambiente estável.

Uma instalação SaaS compartilhada é atualizada como plataforma; tenants e Hub não têm autonomia para escolher versões diferentes do mesmo processo/serviço. A UI apresenta impacto real das unidades afetadas.

## C. Procedimento obrigatório por fase

Para **cada fase**:

1. Relacione componentes existentes reaproveitáveis e riscos; defina pequeno slice vertical e contratos de aceitação.
2. Crie/atualize documentação mínima, testes que falham no início quando apropriado, código e interfaces.
3. Execute format/lint/build/testes unitários/integrados relevantes em ambiente apropriado; capture evidências, sem colar segredos.
4. Execute testes com PostgreSQL real sempre que acesso, migrações, tenancy ou RLS estiverem em jogo. Teste Tenant A/B, leitura/escrita cruzada, membership/grant revogado, Hub delegado e autorização de plataforma. O app nunca usa usuário dono/superuser para consultas comuns.
5. Execute revisão independente com Codex/subagente quando disponível; incorpore correções e repita testes.
6. Atualize documentação, OpenAPI/AsyncAPI, ADR quando requerido, `.changes/`, CHANGELOG, handoff atual, sem substituir documentação alheia. Commit pequeno e coerente **na branch isolada**.
7. Atualize `docs/release/IMPLEMENTATION-STATUS.md` com `PASS`, `FAIL`, `BLOCKED` ou `NOT_APPLICABLE`, caminhos de evidência, SHA do commit e próximo slice; não marcar PASS se não foi testado.
8. Continue automaticamente para a fase seguinte. Fase dependente de `BLOCKED` deve ser registrada como tal; execute em paralelo tarefas independentes que não dependam do gate faltante.

## D. Fases de implementação — NÃO OMITIR

### F0 — Auditoria real e baseline imutável

- Ler a especificação e hierarquia canônica.
- Inventariar branches/worktrees e preservar estado inicial.
- Auditar fluxos reais de deploy local e staging; verificar quais containers/ports/volumes têm dados persistentes e como se faz atualmente backup/restore.
- Auditar migrations: `tools/migrate-sql.sh` e `schema_migrations`, checksums ausentes, scripts legados de `Makefile`, transações, modo de acesso ao owner e schema drift.
- Auditar frontend Vite, Go API/worker, worker lifecycle, NATS/JetStream, health endpoints, control plane de autenticação, rotas `/internal/*`, RLS e Hub.
- Verificar `docs/AI_HANDOFF.md` e `docs/AI_WORKFLOW.md` se existentes; não inventar seu conteúdo.
- Entregar mapa de arquitetura atual, riscos e traceabilidade da baseline em `docs/release/00-audit.md`.

### F1 — ADRs, schemas e design executável

- ADR do sistema de release/update com boundaries, security, trust, deployment model e arquitetura de dados global.
- Contrato `components.yaml` que liga módulos a build/deploy units reais.
- JSON Schema da release, signatures/provenance e policy de canais. Codificar verificador estrito: schema, versão, digests, URLs/identidade permitidas, rollback class, dependências, cadeia de confiança, revocation.
- API contract, state machine, regras de idempotência, locks, heartbeats/fencing, agentes e recovery.
- Testes de manifests válidos/inválidos e matrizes de upgrade.

### F2 — CI completo

- Criar workflows de PR, main, análise de segurança e scripts de validação reutilizáveis.
- Gatilhos corretos, isolamento PR, permissões explícitas, ações pinadas de forma segura; não usar secrets no código de PR de fork.
- Go fmt/vet/tests/race/build; frontend `npm ci`, typecheck/lint/Vitest/build/Playwright; contratos REST/AsyncAPI; migrations e checksums; PostgreSQL 16 real e testes RLS/Hub.
- Construir jobs determinísticos para desenvolvimento paralelo, com concorrência por branch/PR e artefatos de evidência.
- CI pode bloquear novos merges por regressões introduzidas; problemas legados detectados devem ser classificados e não encobertos.

### F3 — Builder de releases e cadeia de suprimentos

- Criar builds OCI para workloads efetivos por SHA; frontend Vite; API e worker. Reutilizar Dockerfiles, corrigir apenas lacunas de produção comprovadas.
- Publicar por digest em GHCR quando credencial/permissão existente; não confiar em `latest`.
- Gerar SBOM, scan, provenance/attestation/signature, verificação de identidade e políticas (GitHub Actions de pipeline confiável ou equivalente).
- Gerar release manifest imutável com release notes e checksums e release candidate. Promotion reutiliza digest.
- Criar verificadores locais com fixtures boas/alteradas/maliciosas.

### F4 — Compatibilidade de DB, backup e restore

- Inspecionar e integrar migrator canônico `tools/migrate-sql.sh` e sua tabela `schema_migrations`; não criar duas fontes de aplicação de SQL. Acrescentar checksums de forma migrável, lint e drift detection conforme necessário.
- Não rodar `make migrate-down` em recovery. Não modificar arquivo `.up.sql` já aplicado sem estratégia de compatibilidade.
- Implementar testes de upgrade N-1, schema expand/contract, compatibilidade com versão antiga e catalogação de migrations.
- Mapear PostgreSQL, backups de mídia/volumes/configuração e chaves de runtime; backup criptografado/externo quando infraestrutura disponível; restauração de laboratório e checksums.
- Base backup/WAL/PITR somente se topologia configurada; caso ausente, bloquear promoção que exija esse RPO/RTO em vez de inventar proteção.
- Emitir evidência objetiva de backup verificado e planos de restauração.

### F5 — Update Agent resiliente e recovery CLI

- Criar agente independente da API e de workers atualizados, sem endpoint remoto de shell, Docker socket público ou scripts dinâmicos injetáveis pelo manifesto.
- Serviço supervisionado, persistência operacional fora de PG, lock/fencing/heartbeat, entradas verificadas e journal de checkpoints.
- Adapter Docker Compose adequado à topologia real com dry-run, download por digest, snapshot de configuração, ordem de restart, drain, health/readiness, smoke, observe e commit/rollback.
- Suportar rollback dos digests/arquivos de configuração **somente quando DB compatível**. Se dados exigem restauração, deixar `RECOVERY_REQUIRED`, preservar evidências e invocar runbook; jamais restauração automática destrutiva.
- CLI local segura para observar status/reconciliar/reinstalar digest anterior compatível quando API indisponível.
- Testar power-loss/crash/restart e jobs duplicados com ferramentas controladas de fault injection.

### F6 — Backend e autorização global

- Implementar release service, planner, preflight, update runs/event store, comunicação autenticada com agente e histórico/auditoria.
- Endpoints versionados conforme padrões existentes com OpenAPI, paginação, idempotency keys, validação, rate limit e SSE/polling.
- Criar/usar permissão **platform admin/update operator**, nunca membership de tenant/Hub ou API Headless como autorização global. Não aceitar `tenant_id` de payload como autoridade.
- Migrations das tabelas de plataforma com políticas de acesso segregadas; evitar vazamento por RLS, queries inseguras ou `BYPASSRLS` no runtime.
- Testar autorização negativa de tenant admin, Hub delegado, operador K3G e CRM externo; testes cross-tenant.

### F7 — Frontend real do OMNIRA

- Criar menu `Administração global → Plataforma → Versões e Atualizações` no React/Vite existente, não substituir app shell nem arquitetura do protótipo Lovable.
- Tela de versão atual, catálogo, changelog, diff, riscos, preflight, confirmação/escopo global, acompanhamento SSE, erros, histórico e recuperação segura.
- Não dar botão de atualização para papel que não tem permissão. Confirmação é server-validated, não depende de condição visual do browser.
- Responsividade, a11y, estados de rede e reentrada de tela com run em progresso; testes Vitest/Playwright.

### F8 — Staging e testes realistas de atualização/falha

- Criar docker-compose de teste/staging isolado e fixtures sanitizadas, sem volumes reais de produção.
- Simular release anterior -> nova, aplicar migrations e smoke, verificar ingressos, UI, API, NATS, webhooks, CRM Headless e Hub.
- Fault injection: digest adulterado, registry offline, disco insuficiente, migration drift, deadlocks/lock, agent kill -9, API off, NATS lag, crash no meio de deploy, health timeout, incompatibilidade DB.
- Comprovar recuperação `R0–R2` e bloqueio seguro `R3/R4`; medir RPO/RTO de lab. Não declarar sucesso sem evidência.

### F9 — Catálogo stable, política, hardening e revisão independente

- Gates de assinatura, revogação, anti-replay, retenção, garbage collection segura e canais.
- Monitoramento: status do agente, falhas de backup, versão real/digest divergente, update estagnado, erros após release.
- Validar segurança de GitHub Actions, artifacts, autenticação, driver de deploy, SSRF, path traversal, injection e segredo em logs.
- Solicitar revisão independente Codex para alterações de alto risco; corrigir findings; reportar qualquer revisão indisponível.

### F10 — Operacionalização e documentação

- Runbooks: release, promote, rollback, recovery sem API, restore/PITR e limites, primeira instalação do agente, CI/GHCR, approval de produção, revocation e troubleshooting.
- Atualizar Makefile com targets seguros de dry-run/status/test e bloquear comandos legados perigosos no fluxo de update sem apagá-los indevidamente.
- Alinhar docs canônicas e novos contratos, exemplos de configuração **sem segredos**, notas de release, changelog, scripts locais e dev onboarding.

### F11 — Aceitação e entrega

- Executar bateria final: build + unit + integrações + RLS/PostgreSQL + web + E2E + upgrade/downgrade policies + backup/restore lab + fault injection + segurança + Codex review.
- Produzir `docs/release/ACCEPTANCE-REPORT.md`: matrizes de cobertura, PASS/FAIL/BLOCKED, evidence IDs, comandos, commit SHA, image digest, gaps, RPO/RTO medidos, limites de rollback, runbooks.
- Preparar commits e PR do branch isolado conforme permissões; nunca merge à força em branch protegida. Se infraestrutura/proteção GitHub/credenciais externas indisponíveis, fornecer arquivo/config/instruções e `BLOCKED_EXTERNAL` para ativação, sem parar código local testável.
- Não marcar produção atualizada. **Entrega** significa capacidade operacional implementada e validada em ambiente seguro, pronta para homologação/ativação controlada pelo operador autorizado.

## E. Regras rígidas de segurança e recuperação

- A identidade do agente verificador, release manifest e digests deve ser validada **antes** de baixar/executar. Release assinatura inválida = BLOCK.
- Privilegiado de Docker = equivalente root. Isolar execução no host; não expor Docker API/socket a runtime ou web.
- API nunca executa shell ou docker diretamente; enfileira pedido autorizado e recebe progresso do serviço independente.
- Release deve ter política explícita para migrations compartilhadas. `down.sql` não autoriza rollback de dados.
- PostgreSQL App usuário normal (com RLS) e migration owner segregado. Nenhum bypass de RLS por conveniência.
- Backup real inclui DB + arquivos/mídias + configuração/segredos necessários à restauração + volumes/event logs quando aplicável. Descrever inconsistências e limites. Não gravar segredos nos backups de CI ou outputs públicos.
- Fail-closed em signature, compatibility, drift, backup, saúde, RBAC e locks.
- Não prometer zero downtime se ambiente só dispõe de Compose single-host; mostrar impacto real e janela apropriada.
- Não alegar rollback de PostgreSQL seguro sem restore/compatibilidade testados.

## F. Política de decisões sem intervenção humana

Resolva autonomamente escolhas de implementação reversíveis e decisões cobertas por código/ADR existentes. Faça ADR e continue slices independentes quando identificar mudança de arquitetura ou segurança. Pergunte apenas quando o plano encontra duas interpretações materialmente incompatíveis que não podem ser resolvidas pela fonte canônica, ou um ato perigoso realmente exige autorização. Quando não houver GHCR tokens, GitHub Environments/Settings, credenciais do host ou servidor staging, conclua os contratos/scripts e testes locais, classifique `BLOCKED_EXTERNAL` e documente o passo manual estritamente necessário; **não solicite usuário para cada bloqueio de ambiente**.

## G. Formato de acompanhamento

Mantenha `docs/release/IMPLEMENTATION-STATUS.md` atualizado:

```
Fase: F<n>
Baseline SHA:
Branch/worktree:
Implementação:
Arquivos:
Migrations:
Contratos/endpoints:
Testes executados (comandos, PASS/FAIL/BLOCKED):
Gates de segurança / RLS:
Review Codex (evidência real ou indisponível):
Commits:
Pendências/risco:
Próxima fase:
```

Ao final apresente **um relatório consolidado único** que indique claramente o que está funcionando, o que foi simulado ou validado em lab, o que foi realmente conectado ao GitHub/GHCR, o que depende de infraestrutura externa e o que **não** foi implantado em produção. Preserve o histórico de decisões e os arquivos de handoff.

## H. Critério absoluto de conclusão

Não considere a missão encerrada após configurar somente CI ou apenas UI. O sistema final deverá conectar:

1. alterações de código isoladas em branches e verificação real no CI;
2. builds versionados por commit com digest e proveniência verificável;
3. catálogo assinado com dependências de módulos/build/deploy units;
4. painel do OMNIRA com RBAC de plataforma e histórico;
5. Update Agent independente que executa preflight e deploy com segurança;
6. migrations e backups verificáveis sem risco de replay/destruição automática;
7. recuperação/rollback compatível e política `RECOVERY_REQUIRED` para dados;
8. testes adversariais multi-tenant, Hub, contratos Headless e falhas reais;
9. docs/runbooks, observabilidade, revisão independente e relatório de evidências.

**Inicie pela F0 agora, avance autonomamente fase a fase e faça todas as correções necessárias sem me consultar, exceto diante de ambiguidade essencial ou autorização explícita exigida.**


---

# PARTE II — PLANO MASTER NORMATIVO

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
