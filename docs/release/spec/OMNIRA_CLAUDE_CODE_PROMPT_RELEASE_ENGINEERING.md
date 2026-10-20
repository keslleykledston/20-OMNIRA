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
