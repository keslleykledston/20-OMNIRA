# Status de implementação — OMNIRA Release Engineering

Formato por fase conforme `docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md` §G.

---

## F0 — Auditoria real e baseline imutável

```
Fase: F0
Baseline SHA: 714c1145b999b97628b64e24906b0c2fee53c55a
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação: auditoria somente-leitura completa (git/worktrees, migrations, backend Go/RLS/authz,
  frontend, deploy/backup/volumes, governança Helm, docs canônicos, ferramentas disponíveis)
Arquivos: docs/release/00-audit.md (novo)
Migrations: nenhuma alterada (fase somente leitura)
Contratos/endpoints: nenhum alterado
Testes executados: nenhum (fase não tem código para testar; achados verificados via leitura direta
  e comandos read-only: git, crontab -l, curl de endpoints read-only, docker ps/inspect via sudo -n)
Gates de segurança / RLS: N/A nesta fase (auditoria apenas); achados de RLS documentados em
  docs/release/00-audit.md §3.2
Review Codex: não executado nesta fase (F0 é baixo risco / somente leitura; a especificação pede
  revisão Codex ao fim de fase CRÍTICA — reservado para F1 em diante)
Commits: (ver git log da branch feat/omnira-release-update-engine)
Pendências/risco:
  - Cron de backup do banco (scripts/backup-omnira-db.sh) e de monitoramento WAHA
    (scripts/waha-session-check.sh) não estão instalados neste host (crontab -l vazio) —
    achado operacional, fora do escopo de correção deste programa, recomendado ao operador.
  - helm/omnira/ existe sem ADR aprovado, contradiz CLAUDE.md — não usado como referência,
    não removido nesta fase.
  - gh CLI não instalado; credenciais GHCR não confirmadas — F2/F3/F11 terão itens
    BLOCKED_EXTERNAL para publicação real.
Próxima fase: F1 — ADR (ADR-0054), release/components.yaml, JSON Schemas
```

**Veredito: PASS**

**Addendum (2026-10-20, início de F1):** antes de iniciar F1, esta branch foi atualizada com
`git merge main` (13 commits à frente desde a baseline do F0: ADR-0050–0053 da API externa,
correção de OIDC, deploy real dessas migrations em produção) — sem conflitos, sem tocar
alterações de outras branches/worktrees. Durante essa atualização, uma checagem read-only
(`scripts/waha-session-check.sh`) mostrou a sessão WAHA do stack principal (`20-omnira-waha-1`)
como **`STOPPED`**, não `WORKING` como registrado no F0 original (§0) — mudança de estado
operacional ocorrida *depois* do F0, não um erro do F0. Nenhuma ação de remediação foi tomada
(fora do escopo deste programa e requer aprovação humana explícita, conforme
`docs/operations/PILOT-RUNBOOK.md` §8); reportado ao dono do produto na mesma sessão. Consequência
para F8: o isolamento de staging continua obrigatório independentemente deste estado.

---

## F1 — ADRs, schemas e design executável

```
Fase: F1
Baseline SHA: d8776dc (merge de main, commit 748528a, para dentro desta branch — ver addendum acima)
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação: ADR-0054 aceito (boundaries, trust model, deployment model, arquitetura de dados
  global, máquina de estados/classes de recuperação por referência); release/components.yaml
  (module_id/build_unit/deploy_unit reais, vocabulário de docs/architecture/MODULE-MANAGEMENT.md);
  release/schemas/release-manifest.schema.json (JSON Schema estrito, additionalProperties:false);
  release/policies/{channels,compatibility,signing}.yaml (3 novas); internal/platformrelease/domain
  (ParseManifest/Validate + máquina de estados RunState/ValidateTransition, zero imports de
  HTTP/pgx/NATS, conforme docs/architecture/GO-PROJECT-STRUCTURE.md)
Arquivos:
  docs/adr/0054-platform-release-update-engine.md (novo; renumerado de um rascunho "0050" que
    colidia com o ADR-0050 já aceito em main nesta mesma sessão — corrigido antes de comitar)
  docs/adr/README.md (índice +0054)
  release/components.yaml, release/schemas/release-manifest.schema.json (herdados de um commit
    anterior desta branch, revisados e com a referência de ADR corrigida)
  release/policies/channels.yaml, release/policies/compatibility.yaml, release/policies/signing.yaml (novos)
  internal/platformrelease/domain/manifest.go (herdado, com 7 correções desta fase — ver Review Codex)
  internal/platformrelease/domain/manifest_test.go (herdado; fixture commit_sha corrigida de 64
    para 40 hex chars — bug real que fazia 2 testes falharem; +11 testes novos dos achados do Codex)
  internal/platformrelease/domain/state_machine.go, state_machine_test.go (novos: RunState,
    tabela de transições legais construída a partir da sequência §6.1 da especificação, 9 casos
    de teste incluindo happy path completo, bloqueio de preflight, cancelamento só antes da trava,
    e o caminho de recuperação nunca pulando RECOVERING)
Migrations: nenhuma (F1 é design/contrato; tabelas platform_* ficam para F6, conforme ADR-0054 §2.4)
Contratos/endpoints: nenhum HTTP ainda (API de /api/v1/platform/* é F6); contrato de dados é o
  JSON Schema + o parser Go desta fase
Testes executados (comandos, PASS/FAIL):
  cd internal/platformrelease && gofmt -l . && go vet ./... && go test ./... -v
    → 35/35 PASS (26 de manifest_test.go incluindo os 11 novos dos achados do Codex, 9 de
      state_machine_test.go)
  go build ./... (repo inteiro) → PASS, sem regressão em nenhum outro pacote
  python3 -c "import yaml; yaml.safe_load(...)" em release/components.yaml e nas 3 policies → OK
  2 bugs reais encontrados e corrigidos nesta fase, ANTES da revisão Codex (fixture de teste
    com commit_sha de 64 hex chars em vez de 40 — SHA de git não é um digest sha256; gofmt
    não aplicado ao código herdado)
Gates de segurança / RLS: N/A ainda (sem tabela de banco nesta fase); o ADR-0054 §2.2 já fixa a
  regra (platform_* sempre FORCE ROW LEVEL SECURITY + is_platform_operator(), nunca BYPASSRLS) a
  ser cumprida em F6
Review Codex (evidência real): `codex exec review --uncommitted` executado 6 vezes nesta mesma
  worktree (um ciclo fix→retest→re-review por achado, nunca aceitando um achado sem corrigir e
  testar antes de pedir a próxima rodada). 7 achados reais [P2] em 5 rodadas, todos corrigidos e
  cobertos por teste novo; a 6ª rodada voltou "No actionable defects were identified":
    1. `dec.More()` não detecta um delimitador de fechamento solto (`}`/`]`) após o manifesto —
       trocado por decodificar mais um valor e exigir io.EOF explicitamente.
    2. Campos "required" do schema JSON (ex. `modules_changed`) sem checagem de presença/não-
       nulidade no validador Go — um manifesto com o campo ausente ou `null` passava; adicionado
       requiredTopLevelFields + checagem via decode para map[string]json.RawMessage.
    3. Elemento `null` dentro de `modules_changed`/`compatibility.from_versions` (arrays de
       string) decodificava silenciosamente para `""` — adicionado rejectNullArrayElements.
    4. Go decodifica campo de struct por nome CASE-INSENSITIVE mesmo com DisallowUnknownFields:
       um manifesto com `"channel"` E `"CHANNEL"` (chaves distintas em JSON puro, uma delas
       sobrescrevendo a outra no struct) nunca era pego — adicionado validateExactKeys
       (case-sensitive) em todo nível (raiz, source, compatibility, migrations, rollout, cada
       elemento de artifacts).
    5. `release_notes_ref` (opcional) aceitava `null` explícito como se fosse ausente.
    6. Chave EXATAMENTE repetida no mesmo objeto (ex. dois `"source": {...}` no nível raiz, ou
       `"workflow_run_id"` duas vezes dentro de `source`) não era pega nem pelo map (fica só com
       a última) nem pelo struct (mescla campo a campo, preservando valores da 1ª ocorrência onde
       a 2ª não os menciona) — adicionado rejectDuplicateKeys, um scanner de tokens que rastreia
       cada objeto aberto em qualquer profundidade.
    7. release/components.yaml subestimava o impacto real de release em 4 módulos: `routing`,
       `audit`, `channels` e `hub` têm código importado tanto por `omnira-api` quanto por
       `omnira-worker` (confirmado por grep de import real, não suposição), mas só declaravam um
       dos dois `build_unit`. Corrigido trocando `build_unit` (escalar) por `build_units` (lista,
       em todos os 16 módulos, não só nos 4 corrigidos) e adicionando o binário que faltava nesses 4.
Commits: (ver git log da branch feat/omnira-release-update-engine — commit desta fase citado no
  próximo update deste arquivo)
Pendências/risco:
  - release/policies/signing.yaml declara known_blockers (gh_cli_missing, ghcr_credentials_unconfirmed)
    herdados do achado F0 §7 — publicação real de assinatura fica BLOCKED_EXTERNAL até essas
    credenciais existirem; o design e os testes locais não dependem disso.
  - O cross-check de build_units em release/components.yaml (achado 7 acima) cobriu os 16 módulos
    contra os imports reais de apps/api/cmd/omnira-api/main.go, internal/platform/httpserver/server.go
    e apps/worker/cmd/omnira-worker/main.go — não foi uma auditoria exaustiva de TODOS os caminhos
    de import possíveis (ex. via outro arquivo de wiring); revalidar empiricamente quando o planner
    (F6) estiver implementado e puder expor qualquer divergência restante.
Próxima fase: F2 — CI Quality Gates (workflows .github/workflows/*, scripts de validação locais
  determinísticos replicando o CI, já que gh CLI/credenciais reais ficam BLOCKED_EXTERNAL para
  publicação mas os workflows e scripts em si são implementáveis e testáveis localmente)
```

**Veredito: PASS**

---

## F2 — CI completo

**Escopo desta fase** (relido com cuidado antes de codificar): a definição por fase (§D) pede
"workflows de PR, main, análise de segurança e scripts de validação reutilizáveis" — `release-
build.yml`/`release-staging.yml`/`release-promote.yml`/`release-dryrun.yml` (citados no inventário
cross-cutting §4.2) pertencem a F3/F8 pela própria definição de cada fase (§D), não a F2; ficam
para as próximas fases, não "esquecidos".

```
Fase: F2
Baseline SHA: 5b828d0 (commit de F1 nesta mesma branch)
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação:
  .github/workflows/ci-pr.yml — G1-G5 em todo PR contra main: Go (gofmt diff-scoped, vet, build,
    test -race), frontend (eslint diff-scoped, typecheck+build, vitest), migrations (pairing/
    naming lint), contratos (OpenAPI/AsyncAPI via tools/validate-specs.sh), integração real
    (scripts/test-integration.sh — Postgres 16 + NATS disposable, RLS/Hub cobertos pelos pacotes
    que esse script já roda), scan de segredo bloqueante (trivy).
  .github/workflows/ci-main.yml — os mesmos gates em push para main, mais: relatório de drift de
    gofmt/eslint no repo INTEIRO (informativo, nunca bloqueia — ver Pendências), coverage +
    SBOM de baseline como artefato (reaproveitado por F3, não regerado).
  .github/workflows/security-scheduled.yml — govulncheck (Go), npm audit (frontend), trivy fs
    (vuln+secret+misconfig) com upload SARIF para a aba Security do GitHub; cron semanal +
    workflow_dispatch; relatório, nunca bloqueia (bloqueio fica para release-promote.yml, F3/F9).
  scripts/release/validate-migrations.sh (novo) — lint estático (sem Postgres) de pareamento
    up/down, nomes e números de versão únicos em migrations/*.sql. NÃO é o ledger de checksums
    que F4 constrói — isso é explicitamente fora do escopo desta fase.
  scripts/release/ci-go-fmt-diff.sh, ci-web-lint-diff.sh (novos) — gates de formatação/lint
    ESCOPADOS ao diff do PR (ver "Pendências" abaixo sobre por que um gate de repo inteiro
    quebraria toda PR desde o dia 1).
  web/eslint.config.js (novo) + devDependencies (eslint 9, typescript-eslint, eslint-plugin-
    react-hooks, eslint-plugin-react-refresh) — `npm run lint` não tinha NENHUMA configuração
    antes desta fase (falhava imediatamente com "ESLint couldn't find a configuration file" —
    achado real, não hipotético); agora funciona de verdade.
  .redocly.lint-ignore.yaml (novo) — baseline dos 35 erros + 7 warnings pré-existentes que
    `tools/validate-specs.sh` (redocly lint) já encontrava em contracts/openapi/omnira-v1.yaml
    antes desta fase (achado real do Codex, ver Review Codex) — nunca executado em CI até agora
    porque não existia `.github/workflows/`. Ratchet: qualquer violação NOVA (não presente na
    lista exata de localizações ignoradas) ainda falha o gate — testado injetando um endpoint
    sem operationId num YAML de teste e confirmando que ainda é pego.
Arquivos: ver Implementação acima; nenhuma migration nova.
Migrations: nenhuma.
Contratos/endpoints: nenhum novo; o contrato REST/AsyncAPI existente passou a ser VALIDADO em CI
  pela primeira vez (ver achado do .redocly.lint-ignore.yaml acima).
Testes executados (comandos, PASS/FAIL):
  actionlint .github/workflows/*.yml → limpo (instalado via `go install
    github.com/rhysd/actionlint/cmd/actionlint@latest`; usa shellcheck internamente — também
    instalado — para validar todo `run:` embutido)
  shellcheck scripts/release/*.sh → limpo
  bash scripts/release/validate-migrations.sh → "160 migration(s) checked... OK"; testado também
    contra fixtures sintéticas quebradas (nome inválido, .down.sql órfão, versão duplicada) —
    os 3 casos foram pegos corretamente, e o caso válido não gerou falso positivo
  bash scripts/release/ci-go-fmt-diff.sh <ref> e ci-web-lint-diff.sh <ref> → testados em branch/
    worktree descartável (criada e removida só para o teste, nunca tocando outras branches):
    confirma que só arquivos mudados são checados, que um arquivo sujo É pego, e (depois da
    correção do achado Codex #2) que uma ref inválida FALHA em vez de reportar "OK" por engano
  bash tools/validate-specs.sh → OK (com o ignore file; sem ele, 35 erros + 7 warnings — achado
    documentado, não escondido)
  go vet ./..., go build ./... (repo inteiro) → PASS
  govulncheck -format sarif ./... → executa, SARIF válido, 19 achados reais (maioria "depende
    mas não chama"; alguns "chama função vulnerável" em go-redis/html-template/net-http —
    classificado como achado pré-existente, não corrigido nesta fase, ver Pendências)
  npm audit --audit-level=low (web/) → 16 vulnerabilidades reais (2 critical, 8 high, 6 moderate)
    em devDependencies (vitest/tinypool, source-map-js, react-router) — achado pré-existente,
    mesmo tratamento
  npm run lint (web/, escaneando TUDO, não só o diff) → 47 problemas reais (4 erros, 43 warnings)
    em código pré-existente — classificado, gate real é diff-scoped (ver acima)
Gates de segurança / RLS: nenhuma tabela nova. ci-pr.yml/ci-main.yml rodam
  scripts/test-integration.sh, que já cobre RLS/Hub adversarial nos pacotes existentes (nada
  novo verificado aqui além de ligar isso ao CI pela primeira vez). secret-scan é bloqueante por
  design (um segredo commitado nunca é aceitável, mesmo numa PR "só de CI").
Review Codex (evidência real): `codex exec review --uncommitted` executado 3 vezes nesta branch.
  2 achados reais corrigidos entre as rodadas; 3ª rodada "No actionable defects were identified":
    1. [P1] O gate de contratos (`tools/validate-specs.sh` em ambos os workflows) falhava no
       repositório sem NENHUMA mudança: 35 erros reais de redocly lint já existiam em
       contracts/openapi/omnira-v1.yaml, nunca detectados porque nenhum CI rodava isso antes.
       Corrigido com `redocly lint --generate-ignore-file` (baseline ratcheted, não blanket
       disable — verificado que uma violação NOVA ainda é pega).
    2. [P2] `ci-go-fmt-diff.sh`/`ci-web-lint-diff.sh` tinham `|| true` no `git diff` que
       calculava os arquivos mudados: uma ref base inexistente ou inalcançável (clone shallow,
       digitação errada) fazia o `git diff` falhar, mas o `|| true` transformava isso em "nenhum
       arquivo mudado" — ou seja, um gate que deveria FALHAR por não conseguir checar nada
       reportava sucesso. Reproduzido com uma ref inventada antes de corrigir (exit 0 indevido),
       e confirmado exit 128 correto depois.
Commits: (ver git log da branch feat/omnira-release-update-engine — commit desta fase citado no
  próximo update deste arquivo)
Pendências/risco:
  - Playwright E2E (`npm run test:e2e`) foi DELIBERADAMENTE deixado fora de ci-pr.yml/ci-main.yml.
    Achado real desta fase: `web/playwright.config.ts` tem `baseURL` fixo em
    `https://omnira.devops.k3gsolutions.com.br` — o host real, `LIMITED_INTERNAL_PRODUCTION`
    (docs/release/00-audit.md §0), com WhatsApp conectado de verdade — e os specs usam caminhos
    relativos (`page.goto('/inbox')` etc.), resolvidos contra esse baseURL. Ligar isso ao CI hoje
    mandaria tráfego automatizado real contra produção em todo push. Corrigir isso com um target
    isolado é exatamente o trabalho de F8 (staging); não é patch de F2.
    **Atualização (branch fix/release-eng-legacy-drift, commit 1f8a9c2):** o landmine do default
    de produção em si já foi corrigido — `baseURL` agora usa
    `process.env.PLAYWRIGHT_BASE_URL ?? 'http://127.0.0.1:3000'` (o próprio servidor local que o
    `webServer` do config já inicia). Isso NÃO liga E2E ao CI (ainda é trabalho de F8), só remove
    o risco de uma execução manual/local acidental ir contra produção.
  - `tools/validate-schema.sh` e `tools/check-rls.sh` existentes apontam, por padrão, para
    `omnira-postgres`/`omnira_dev` (o banco real) — não foram usados nestes workflows; os gates
    novos usam exclusivamente containers descartáveis (scripts/test-integration.sh).
  - **Drift pré-existente — accounting final depois da branch `fix/release-eng-legacy-drift`**
    (4 commits: `2b86d2b`, `d8329f3`, `4db6d2c`, `6f2486d`; branch separada, nunca tocou
    `feat/omnira-release-update-engine` nem nenhuma outra worktree):
    - **gofmt: 42 de 42 corrigidos** (100%). `gofmt -w` puro, sem mudança de comportamento;
      `go build`/`go vet`/`go test ./...` completos, sem FAIL, depois da correção.
    - **ESLint: 2 de 4 erros reais corrigidos** (os 2 que exigiam só entendimento + correção
      pontual: `no-control-regex` em MessageMedia.tsx — comportamento já estava certo, só
      silenciado com eslint-disable + comentário; `no-empty-object-type` em types/api.ts —
      interface vazia trocada por type alias). As 43 **warnings** continuam abertas de propósito
      — cada uma exige julgamento caso a caso sobre dependência de hook/export, não é "corrigir
      e seguir"; não tocadas.
    - **govulncheck: 3 de 19 achados corrigidos** (go-redis v9.7.0→v9.7.3 — o único com caminho
      de chamada real no código do projeto —, grpc v1.83.1→v1.83.2, klauspost/compress
      v1.18.5→v1.18.7). **16 permanecem, explicitamente não corrigíveis sem uma decisão maior**:
      12 são da própria biblioteca padrão do Go (precisam do toolchain go1.26.9, hoje
      go1.25.14 instalado — e os Dockerfiles pinam `golang:1.25-alpine`); os outros são
      `golang.org/x/net`@v0.60.0 e `golang.org/x/crypto`@v0.56.0, cuja correção existe mas cada
      uma delas exige `go 1.26.0` no próprio go.mod (testado e revertido nesta branch antes de
      prosseguir — ver commit `4db6d2c`); e GO-2026-5932 em x/crypto não tem fix publicado ainda
      ("Fixed in: N/A"). Bump de toolchain Go é uma decisão de infraestrutura separada (afeta
      Dockerfile.api/Dockerfile.worker e qualquer imagem CI pinada), não uma correção de
      dependência pontual — recomendado como item próprio ao dono do repositório.
    - **npm audit: 2 de 16 vulnerabilidades corrigidas** (`npm audit fix`, sem `--force`:
      follow-redirects e postcss-selector-parser, dentro das faixas semver já declaradas). As
      14 restantes (6 moderate, 6 high, 2 critical) exigem `npm audit fix --force` — o próprio
      npm avisa que isso instala tailwindcss@4, react-router-dom@7.18.4 e vitest@5/tinypool,
      todos breaking changes reais que pedem teste de regressão manual à parte; deliberadamente
      não aplicados.
    - **redocly (contratos): sem mudança nesta branch** — já baselined em `.redocly.lint-ignore.yaml`
      no commit original de F2 (`12891ca`); ratchet confirmado (uma violação nova ainda é pega).
  - Publicação real de qualquer workflow (execução no GitHub Actions, push de imagem, SARIF na
    aba Security) continua BLOCKED_EXTERNAL: sem `gh` CLI, sem credenciais confirmadas, sem
    acesso a Settings/Environments deste repositório nesta sessão (achado original do F0,
    inalterado). Todo o restante (sintaxe, lint das próprias workflows, scripts que elas chamam)
    foi verificado localmente, de verdade, não hipoteticamente.
Próxima fase: F3 — Builder de releases e cadeia de suprimentos (release-build.yml: build OCI por
  SHA para api/worker/web, SBOM, provenance/attestation, assinatura, manifesto de release validado
  contra o schema de F1; publicação real fica BLOCKED_EXTERNAL pelas mesmas razões acima)
```

**Veredito: PASS**

---

## F3 — Builder de releases e cadeia de suprimentos

```
Fase: F3
Baseline SHA: 12891ca (commit de F2 nesta mesma branch)
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação:
  apps/releasectl/cmd/releasectl (novo, CLI Go puro — zero HTTP/pgx/NATS): `build-manifest`
    monta um manifesto a partir de flags e SEMPRE o revalida com o mesmo ParseManifest estrito
    de F1 antes de gravar em qualquer lugar (nunca escreve um manifesto que seu próprio
    verificador rejeitaria); `validate-manifest` parseia/valida um arquivo existente.
  release/fixtures/{manifest-good,manifest-tampered-movable-tag,manifest-malicious-unknown-field,
    manifest-malicious-case-variant-key}.json (novos) + scripts/release/test-manifest-verifier.sh
    (novo): exercita o binário real (não só as funções Go) contra bom/adulterado/malicioso —
    pedido explícito desta fase.
  scripts/release/migrations-checksum.sh (novo): sha256 de um INVENTÁRIO ordenado (nome +
    digest de conteúdo) de migrations/*.up.sql — não dos bytes concatenados (ver achados do
    Codex abaixo) — para o campo migrations.checksum_bundle do manifesto.
  .github/workflows/release-build.yml (novo): workflow confiável, build+SBOM+scan+attestation+
    assinatura dos 3 workloads por digest OCI, nunca `:latest`; monta e assina o manifesto de
    release (channel sempre "candidate" — nunca "stable", que é F8/F9); gatilho `workflow_run`
    de "CI (main)" (nunca builda/assina um commit cujo CI não passou) + workflow_dispatch manual
    gated por ambiente aprovado (nome referenciado, criação real do Environment com reviewers é
    BLOCKED_EXTERNAL); mecanismo próprio de baseline de migrations via tag git
    `release-candidate-latest`, movida só após sucesso completo, com checagem de ancestralidade
    (nunca anda pra trás) e concorrência global (nunca duas corridas decidem a tag ao mesmo tempo).
Arquivos: ver Implementação acima; nenhuma migration nova.
Migrations: nenhuma.
Contratos/endpoints: nenhum HTTP novo (API de /api/v1/platform/* é F6). O "contrato" desta fase
  é o manifesto assinado + o binário releasectl que o monta/valida.
Testes executados (comandos, PASS/FAIL):
  go test ./apps/releasectl/... ./internal/platformrelease/... → PASS (8 testes novos do CLI)
  gofmt -l, go vet ./..., go build ./... (repo inteiro) → limpos
  actionlint .github/workflows/*.yml (com shellcheck embutido, ambos instalados via
    `go install`/`apt-get` nesta sessão) → limpo, incluindo release-build.yml
  bash scripts/release/test-manifest-verifier.sh → 4/4 fixtures (bom aceito; 3 adulterados/
    maliciosos rejeitados)
  bash scripts/release/migrations-checksum.sh → determinístico (rodado 2x, mesmo digest);
    testado sensível a rename (fixture sintética) e a diretório ausente/vazio (fail-closed)
  Simulação local completa do pipeline de montagem do manifesto (sem Docker/GHCR reais):
    digests simulados → release-manifest-sim.json → validate-manifest → PASS; testado também
    o split de --from-version por vírgula e a checagem de ancestralidade de tag com SHAs reais
    deste repositório (git merge-base --is-ancestor)
  cosign real baixado e `cosign sign-blob --help` inspecionado nesta sessão para confirmar os
    flags corretos (--bundle), não assumidos de memória — ver achados do Codex
  Fontes reais inspecionadas (não assumidas) para 2 achados do Codex: actions/toolkit
    packages/attest/src/provenance.ts (gitCommit: claims.sha) e aquasecurity/trivy-action
    action.yaml (limit-severities-for-sarif)
Gates de segurança / RLS: N/A (sem tabela de banco nesta fase). Hardening de Actions aplicado:
  toda action de terceiro pinada por SHA completo; ci-pr.yml nunca usa pull_request_target;
  todo input de workflow_dispatch lido via env: (nunca interpolado direto num run:, e validado
  por formato) — 2 achados reais de injeção corrigidos, ver abaixo; ref confiável
  (refs/heads/main|refs/tags/omnira-*) verificado antes de qualquer permissão de escrita/OIDC.
Review Codex (evidência real): `codex exec review --uncommitted` executado 11 vezes nesta
  branch (ciclo fix→retest→re-review a cada achado). 11 achados reais corrigidos em 10 rodadas;
  a 11ª voltou "No actionable regressions were identified":
    1. [P1] `digest.env` de cada unidade da matriz tinha o MESMO nome de arquivo — download com
       merge-multiple sobrescrevia, deixando só 1 de 3 artifacts no manifesto. Corrigido com
       nome de arquivo único por unidade.
    2. [P1] Caminho do Dockerfile do web duplicado (`web/web/Dockerfile`, que não existe) — a
       variável `dockerfile` já era repo-root-relative; corrigido removendo a concatenação com
       `context`.
    3. [P2] VERSION da imagem calculado separadamente do manifesto (podiam divergir) — movido
       para um job `vars` único, computado uma vez, usado nos dois lugares via `needs.vars.outputs`.
    4. [P2] `--from-version` recebia a string inteira separada por vírgula como um valor só —
       corrigido fazendo split e repetindo a flag.
    5. [P1] Nenhuma assinatura/attestation cobria o MANIFESTO (só as imagens) — adicionado
       attest-build-provenance + cosign sign-blob sobre o release-manifest.json.
    6. [P1] workflow_dispatch não restringia a ref — qualquer branch obtinha packages:write/
       id-token:write. Corrigido com checagem explícita (falha alta, nunca skip silencioso)
       contra release/policies/signing.yaml's allowed_ref_patterns.
    7. [P2] Inputs de workflow_dispatch interpolados direto num `run:` (injeção de shell clássica
       do GitHub Actions) — corrigido lendo tudo via `env:` + validação de formato por regex.
    8. [P2] `migrations_start_after`=`migrations_target` (mesmo valor) declarava "nenhuma
       migration nesta release" mesmo quando havia migrations novas — corrigido derivando de
       uma tag git própria (`release-candidate-latest`), não de um palpite.
    9. [P2] Nenhum scan de vulnerabilidade de imagem (só SBOM) — adicionado trivy image scan,
       fail-closed só em CRITICAL (severidade mais alta só bloqueia stable, F9).
   10. [P1] Gate completamente ausente: release-build.yml rodava independente de ci-main.yml —
       um commit que falhasse testes/RLS/contratos/secret-scan ainda seria buildado e assinado.
       Corrigido trocando o gatilho de `push` para `workflow_run` de "CI (main)", com checagem
       de `conclusion == 'success'`.
    (as próximas rodadas, já depois da correção acima, encontraram problemas NESSA correção
    — típico de revisão iterativa real, não um round único raso):
   11. [P1] Flag `cosign sign-blob --output-signature/--output-certificate` não existe nesta
       versão do cosign (confirmado baixando o binário real e lendo `--help`) — corrigido para
       `--bundle`, a forma atual suportada.
   12. [P1] A tag `git rev-parse <ref> || true` imprimia a string literal do ref (não vazio) e
       não falhava de verdade quando o ref não existia — `|| true` escondia isso. Corrigido com
       `git rev-parse --verify -q`.
   13. [P1] Race: dois builds de commits diferentes podiam mover `release-candidate-latest` para
       trás se o mais antigo terminasse depois. Corrigido com `concurrency.group` GLOBAL (não
       por SHA) + checagem de ancestralidade (`git merge-base --is-ancestor`) antes de mover.
   14. [P1] workflow_dispatch nunca exigia o "ambiente aprovado" que o próprio
       release/policies/signing.yaml (F1) já proibia pular — adicionado `environment:` condicional
       nos jobs que assinam (nome referenciado; criação real do Environment é BLOCKED_EXTERNAL).
   15. [P1] `migrations_start_after` manual não era validado antes de ir para $GITHUB_OUTPUT —
       um valor com newline injetaria uma segunda linha de output (ex. sobrescrever
       `resolved_sha`). Corrigido validando contra o formato exato de um stem de migration.
   16. [P1] Mesma classe de injeção em `from_versions`: a string bruta (possivelmente multilinha)
       ainda era reemitida para o output mesmo depois de validada entrada a entrada. Corrigido
       reconstruindo o valor a partir do array já validado, nunca reemitindo o input bruto.
   17. [P1] `trivy-action` não aplica o filtro de severidade a saída SARIF sem
       `limit-severities-for-sarif: true` (confirmado lendo o `action.yaml` real da versão
       pinada) — sem isso, `severity: CRITICAL` não limitava o `exit-code`. Corrigido.
  (17 achados, não 11 — a contagem de "rodadas" conta ciclos de revisão, não achados; alguns
  ciclos trouxeram 2 achados de uma vez.)
  LIMITAÇÃO DOCUMENTADA, NÃO UM BUG CORRIGÍVEL AQUI: o predicado de proveniência de
  `actions/attest-build-provenance` usa `claims.sha` do token OIDC do job (confirmado lendo
  `actions/toolkit`'s `packages/attest/src/provenance.ts` real) — um valor que este workflow
  não controla. Não foi possível confirmar sem uma execução real se esse claim reflete
  `workflow_run.head_sha` (o que foi de fato buildado) quando `main` já avançou. Documentado no
  próprio workflow; precisa de verificação ao vivo antes de confiar na proveniência para algo
  além do teste local deste programa.
Commits: (ver git log da branch feat/omnira-release-update-engine — commit desta fase citado no
  próximo update deste arquivo)
Pendências/risco:
  - Publicação real (push GHCR, execução do Actions, Environment com reviewers configurado,
    verificação ao vivo da proveniência) continua BLOCKED_EXTERNAL — sem gh CLI, sem
    credenciais, sem acesso a Settings deste repositório nesta sessão (achado original do F0).
  - `modules_changed` é sempre `[]` nesta fase (sem planner — isso é F6). `platform_version`/
    `from_versions`/o bootstrap da tag `release-candidate-latest` são esquemas deliberadamente
    simples (documentados no cabeçalho do workflow) até existir histórico real de release.
  - A checagem de ancestralidade/tag e o gate de CI(main) nunca foram exercitados contra uma
    execução real do GitHub Actions — só localmente, com SHAs e cenários simulados à mão.
Próxima fase: F4 — Compatibilidade de DB, backup e restore (integrar tools/migrate-sql.sh como
  único runner usado por automação, acrescentar ledger de checksum por migration sem reescrever
  as já aplicadas, mapear backups de PG/mídia/config, testar upgrade N-1 e restore descartável)
```

**Veredito: PASS**
