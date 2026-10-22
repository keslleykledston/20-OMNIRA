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
    - **ESLint: 4 de 4 erros reais corrigidos** (2 na branch `fix/release-eng-legacy-drift` —
      `no-control-regex` em MessageMedia.tsx, comportamento já estava certo, só silenciado com
      eslint-disable + comentário; `no-empty-object-type` em types/api.ts, interface vazia
      trocada por type alias — e 2 encontrados só depois do merge de volta nesta branch, ao
      revalidar `npm run lint` com 0 erros esperados e encontrando 1 ainda aberto:
      `prefer-const` em `AccountsPage.test.tsx`, `let tickets` nunca reatribuído, trocado por
      `const`, 9/9 testes daquele arquivo confirmados depois). `npm run lint` agora termina em
      **0 erros**. As 43 **warnings** continuam abertas de propósito — cada uma exige julgamento
      caso a caso sobre dependência de hook/export, não é "corrigir e seguir"; não tocadas.
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

---

## F4 — Compatibilidade de DB, backup e restore

```
Fase: F4
Baseline SHA: 7c677f4 (commit mais recente desta mesma branch)
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação:
  tools/migrate-sql.sh (modificado): ledger de checksum por migration — coluna `checksum`
    aditiva (ALTER TABLE ... ADD COLUMN IF NOT EXISTS, nunca reescreve linhas já existentes);
    toda migration aplicada por `up` (ou marcada via BASELINE_UP_TO) grava o sha256 do arquivo
    no momento da aplicação. Novo subcomando `verify`: recomputa o sha256 de cada migration
    APLICADA e compara com o gravado — MODIFIED/MISSING bloqueiam (exit 1), UNKNOWN (linhas
    aplicadas antes deste recurso existir) é só informativo, nunca falha. `up` agora chama essa
    mesma verificação ANTES de aplicar qualquer migration pendente (achado Codex: só existir em
    `verify` não bloqueava a instalação normal, que é o que a especificação pede). Novo
    subcomando `backfill-checksums`: opt-in, exige CONFIRM_BACKFILL=1, nunca chamado por
    automação, grava o checksum de hoje em linhas pré-existentes sem checksum — não é
    "reescrever uma migration já aplicada" (o arquivo .up.sql nunca é tocado), é dar à linha do
    ledger seu primeiro checksum, com a suposição explícita e documentada de que o arquivo no
    disco hoje é o mesmo que foi aplicado.
  tools/apply-migrations.sh, tools/migrate.sh (removidos): "duas fontes de aplicação de SQL"
    que a especificação proíbe explicitamente. apply-migrations.sh não tinha ledger de
    idempotência nem ON_ERROR_STOP (psql sem -v ON_ERROR_STOP=1 continua após um erro dentro do
    arquivo) e era a base real usada por scripts/test-integration.sh e
    scripts/test-archive-wa-groups.sh — migrados para tools/migrate-sql.sh (único caller real).
    migrate.sh era um wrapper morto (zero callers) para golang-migrate, com um ESQUEMA DE LEDGER
    INCOMPATÍVEL (schema_migrations `version bigint, dirty boolean` do golang-migrate vs.
    `version TEXT, applied_at, checksum` deste projeto) — puro risco, sem uso real.
  scripts/test-integration.sh, scripts/test-archive-wa-groups.sh (modificados): usam
    tools/migrate-sql.sh com MIGRATIONS_DIR explícito (achado Codex: sem isso, o runner padrão
    `/migrations` absoluto não existe no host, quebrando os dois únicos callers reais de
    apply-migrations.sh). test-integration.sh agora também roda `migrate-sql.sh verify` logo
    após montar o template, exercitando a detecção de drift em TODO run de CI, não só num teste
    sintético isolado.
  scripts/release/lint-migration-safety.sh (novo): lint estático (heurístico, documentado como
    tal, não um parser de SQL) que compara o DDL de uma migration contra o database_strategy
    que uma release declara (release/policies/compatibility.yaml). Detecta, com escopo por
    tabela.coluna e respeitando a ordem das instruções no arquivo: DROP TABLE/COLUMN (com ou sem
    a palavra-chave COLUMN, que o Postgres torna opcional), RENAME COLUMN/TO, TRUNCATE (distinto
    do nome de privilégio homônimo em GRANT/REVOKE), DROP VALUE de enum, e a família completa
    NOT NULL/DEFAULT: ADD COLUMN com NOT NULL inline sem DEFAULT, SET NOT NULL numa coluna
    pré-existente ou adicionada sem DEFAULT no mesmo arquivo, DEFAULT NULL (literal ou
    `(NULL)`) tratado como "sem DEFAULT real", DROP DEFAULT/SET DEFAULT NULL que revoga a
    compatibilidade de uma coluna NOT NULL, e SET DEFAULT (via ALTER COLUMN, não só inline)
    como uma forma igualmente válida de dar compatibilidade. Nomes de tabela com schema
    (`public.foo`) são capturados por completo, nunca só o primeiro segmento. 17 achados reais
    de 13 rodadas de revisão independente do Codex, todos corrigidos e re-testados (lista
    completa abaixo).
  release/fixtures/migration-lint/*.up.sql (16 fixtures novas): bons e maus exemplos de cada
    padrão acima, exercitados por scripts/release/test-lint-migration-safety.sh — pedido
    explícito desta fase ("fixtures boas/alteradas/maliciosas", mesmo espírito do F3).
  release/policies/backup.yaml (novo): declara o que uma promoção de canal exige como evidência
    de backup/restore por classe de recuperação (R0–R4, ADR-0054 §2.5) — reaproveita
    scripts/backup-omnira-*.sh e scripts/backup-restore-*.sh já existentes, nunca os substitui
    (ADR-0054 §2.1). Declara o estado REAL deste host: backup lógico (pg_dump), sem WAL/PITR,
    sem cron instalado (F0 §5.3, reconfirmado) — e por isso fixa min_rpo_minutes=60 (honesto
    para dump periódico manual), nunca os 5 min alvo de docs/operations/DISASTER-RECOVERY.md,
    que dependem de PITR ausente aqui. "Bloquear promoção que exija esse RPO/RTO em vez de
    inventar proteção" (pedido explícito da fase) — nenhuma promoção "stable" pode declarar um
    RPO menor que este sem um ADR que troque current_topology primeiro.
  scripts/release/test-migration-ledger.sh, test-upgrade-n1.sh, test-lint-migration-safety.sh
    (novos): ver Testes executados abaixo.
Arquivos: ver Implementação acima; nenhuma migration nova (o ledger de checksum é aditivo via
  ALTER TABLE ... ADD COLUMN IF NOT EXISTS dentro do próprio tools/migrate-sql.sh, não um
  arquivo numerado em migrations/ — schema_migrations não é uma tabela de aplicação, é
  infraestrutura do próprio runner).
Migrations: nenhuma.
Contratos/endpoints: nenhum (a API de /api/v1/platform/* é F6).
Testes executados (comandos, PASS/FAIL, Postgres 16 real via containers descartáveis):
  bash scripts/release/test-lint-migration-safety.sh → PASS (18 fixtures + varredura completa
    das 160 migrations reais do repositório sob expand_only: exatamente 1 achado, o pré-existente
    documentado abaixo, nada mais — zero falso positivo novo, zero falso negativo conhecido)
  sudo bash scripts/release/test-migration-ledger.sh → PASS (12 checks, Postgres 16 descartável
    real): primeira aplicação grava checksum em toda linha nova; segunda aplicação (retry) é
    idempotente, 0 migrations aplicadas, 0 linhas novas no ledger; `verify` limpo; adulterar um
    arquivo já aplicado é detectado por `verify` E bloqueia `up` de aplicar QUALQUER coisa
    (inclusive uma migration nova, nunca antes aplicada); apagar um arquivo já aplicado é
    detectado; uma linha sem checksum (simulando ledger pré-existente ao recurso) é UNKNOWN, não
    falha; `backfill-checksums` recusa sem CONFIRM_BACKFILL=1 e grava corretamente com ele
  sudo bash scripts/release/test-upgrade-n1.sh → PASS (7 checks, Postgres 16 descartável real):
    checkpoint real 000168 (último estado de schema que rodou em produção antes deste programa,
    não um número inventado) → aplica os 4 migrations seguintes → dado escrito em 000168
    sobrevive integralmente ao upgrade; FORCE RLS inalterado; ledger termina na migration real
    mais nova; `verify` limpo; os 4 migrations pós-checkpoint são expand_only-limpos pelo lint
  bash scripts/release/validate-migrations.sh (F2, re-executado) → PASS, 160 migrations, sem
    regressão
  gofmt -l / go build ./... / go vet ./... (repo inteiro) → limpos
  shellcheck -S warning em todos os scripts novos/modificados → limpo
  scripts/backup-restore-check.sh (já existente, reaproveitado como evidência real desta fase,
    rodado contra o omnira-postgres real — só bancos descartáveis, nunca omnira_dev) → PASS:
    dump+restore no mesmo cluster E em cluster novo (disaster recovery), checksums de 19 tabelas
    críticas + flags RLS/FORCE/policies/funções/ledger idênticos, isolamento cross-tenant
    comprovado como omnira_app pós-restore. RTO medido: ~6,4s (mesmo cluster), ~6,8s (cluster
    novo, incl. roles).
  scripts/backup-omnira-db.sh (rodado de verdade, read-only sobre omnira_dev) → backup real
    produzido: backups/omnira_dev/omnira_dev_20261021T021842Z.dump (6019786 bytes),
    schema_version=000172_external_start_conversation
  scripts/backup-restore-hub-check.sh (rodado com o dump real acima, cluster novo descartável)
    → PASS: ledger do restaurado = migration mais nova do repositório; tabelas de controle do
    Hub com a mesma contagem do live; modo de contexto de sessão (`enforce`) sobreviveu ao
    restore; agente do Hub lê exatamente o oráculo independente permite (190 linhas); estranho e
    identidade forjada leem 0. RTO medido: ~8,1s (cluster novo, com o dump real de produção).
Gates de segurança / RLS: N/A nesta fase para RLS nova (sem tabela de plataforma ainda — F6).
  Reaproveitada e comprovada de novo a RLS/isolamento das tabelas existentes via
  backup-restore-check.sh/backup-restore-hub-check.sh acima (nenhuma regressão pelo ledger de
  checksum ou pelas mudanças em migrate-sql.sh).
Review Codex (evidência real): `codex exec review --uncommitted` executado 13 vezes nesta
  branch (ciclo fix→retest→re-review a cada achado, mesma disciplina do F3). 17 achados reais
  corrigidos; a 13ª rodada voltou "No actionable regressions were identified":
    1. [P1] `up` nunca checava drift de checksum — só `verify`, separado, que um operador podia
       nunca rodar. Corrigido: `up` chama a mesma verificação ANTES de aplicar qualquer
       pendente, e passa a bloquear também uma migration NOVA se uma antiga estiver adulterada.
    2. [P2] Lint aceitava SET NOT NULL numa coluna adicionada sem DEFAULT no mesmo arquivo (a
       exceção só olhava o nome da coluna, não a tabela) — corrigido com chave tabela.coluna.
    3. [P2] A mesma exceção ignorava NOT NULL inline em ADD COLUMN (só olhava ALTER COLUMN
       separado) — corrigido, ADD COLUMN ... NOT NULL sem DEFAULT agora é achado por si só.
    4-9. [P2 x6] Six achados sucessivos de borda real da mesma família NOT NULL/DEFAULT, cada um
       reproduzido e corrigido: nome de tabela com schema (`public.a` colapsando com
       `public.b`), `DEFAULT NULL` tratado como DEFAULT real, COLUMN opcional em DROP/ADD
       (`ALTER TABLE t DROP email` sem a palavra COLUMN) escapando do lint, delimitador de fim
       de clausula não reconhecendo ADD/DROP sem COLUMN (vazando DEFAULT de uma coluna para
       outra no mesmo ALTER TABLE), DROP DEFAULT não revogando a exceção de uma coluna já NOT
       NULL (nem quando o NOT NULL veio inline do próprio ADD COLUMN), SET DEFAULT via ALTER
       COLUMN (não só inline) nunca contando como compatível.
   10. [P2] Normalização de espaço em branco: `SET  DEFAULT` (dois espaços) comparado por
       igualdade exata de string caía no ramo errado (tratado como remoção de DEFAULT) —
       corrigido normalizando antes de comparar.
   11. [P2] `DROP NOT NULL` nunca era reconhecido — uma coluna devolvida a nullable continuava
       marcada como NOT NULL internamente, gerando falso positivo num DROP DEFAULT seguinte —
       corrigido.
   12. [P2] `DEFAULT (NULL)` (parenteses) escapava do mesmo tratamento de `DEFAULT NULL` —
       corrigido.
   13. [P2] Os checks simples (DROP TABLE, RENAME, TRUNCATE) usavam `grep` padrão, que casa uma
       LINHA por vez — uma instrução destrutiva formatada em múltiplas linhas (`DROP` numa
       linha, `TABLE widgets` na próxima) escapava do lint inteiramente. Corrigido normalizando
       todo whitespace (inclusive quebras de linha reais) para um espaço antes desses checks; o
       check de TRUNCATE foi reescrito para identificar a instrução pelo que a segue (um
       identificador, nunca "ON") em vez de excluir linhas com GRANT/REVOKE, que no texto
       normalizado apagaria o check inteiro se GRANT/REVOKE aparecesse em QUALQUER lugar do
       arquivo.
  LIMITAÇÃO ACEITA E DOCUMENTADA NO PRÓPRIO SCRIPT (não um achado corrigível sem transformar um
  heurístico declarado em parser completo de SQL): múltiplas sub-clausulas de uma ALTER TABLE
  separadas por vírgula (ex. `ALTER COLUMN x DROP DEFAULT, ALTER COLUMN x SET DEFAULT 2` numa
  única instrução) são avaliadas sub-clausula por sub-clausula, não como o estado atômico final
  que o Postgres realmente aplica — um padrão contorcido e sem uso real neste repositório (a
  forma natural e igualmente segura é escrever só `SET DEFAULT 2`, que o lint já aceita). É a
  direção seguro do viés documentado do próprio script (rejeitar demais um padrão raro, nunca
  aceitar de menos um contrato real).
  ACHADO PRÉ-EXISTENTE, NÃO CORRIGIDO (arquivo já aplicado em produção, reescrever uma migration
  aplicada é proibido): migrations/000104_hub_work_pools.up.sql adiciona `hub_id UUID` (sem
  DEFAULT), faz backfill, e só então `SET NOT NULL` — exatamente o padrão que database_strategy
  expand_only proíbe (um release anterior ainda inserindo sem essa coluna quebraria). É uma
  lacuna real de expand/contract de ANTES deste programa existir, só visível porque o lint desta
  fase agora existe. Classificado, não mascarado, não revertido.
Commits: (ver git log da branch feat/omnira-release-update-engine — commit desta fase citado no
  próximo update deste arquivo)
Pendências/risco:
  - scripts/backup-omnira-db.sh: a cópia para o disco externo (`/mnt/omnira-backup-external`,
    montagem NFS real, 20G livres) foi SKIPPED nesta sessão — não por disco cheio/desmontado,
    mas aparentemente por permissão (o diretório `Backup/` esperado não existe sob o layout real
    do mount, que tem `omnira_dev/`/`groups/` direto na raiz, e o usuário desta sessão não é o
    dono do mount). Não é tratado como achado confirmado de pipeline quebrado: o cron real
    (ainda não instalado, F0 §5.3) presumivelmente roda como outro usuário. Recomendado ao
    operador verificar com o usuário real de cron, não corrigido aqui para não mascarar um
    comportamento cujo contexto real (usuário/cron) esta sessão não tem.
  - Cron de scripts/backup-omnira-db.sh e scripts/backup-omnira-media.sh continua não instalado
    neste host (F0 §5.3, reconfirmado nesta fase) — gap operacional, fora do escopo de correção
    deste programa de código, repetido aqui para que release/policies/backup.yaml nunca
    presuma um agendamento que não existe de fato.
  - PITR/WAL continuam ausentes deste host (alvo documentado em
    docs/operations/DISASTER-RECOVERY.md, não implementado) — release/policies/backup.yaml fixa
    o RPO honesto (60 min) em vez de herdar o alvo de 5 min; destravar isso exige um ADR que
    adote Postgres gerenciado/WAL archiving antes de qualquer promoção "stable" poder prometer
    menos.
  - scripts/test-integration.sh, quando rodado NESTE worktree secundário (não no checkout
    principal /opt/omnira/20-OMNIRA), constrói o template e roda `migrate-sql.sh verify` com
    sucesso (confirmado nesta fase com um pacote real, internal/accounts/adapters), mas o
    estágio seguinte (go test dentro de um container Docker) falha com
    "fatal: not a git repository: .../.git/worktrees/release-engineering" — o arquivo `.git` de
    QUALQUER worktree vinculado aponta para um caminho absoluto do host fora do que é montado no
    container. Isto é uma limitação PRÉ-EXISTENTE do ambiente (afeta qualquer worktree, inclusive
    os de outros agentes, não é causado por nenhuma mudança desta fase) e não uma regressão —
    documentado, não corrigido (mudaria a estratégia de mount do script, fora do escopo de F4).
  - `release/policies/backup.yaml` declara a política; o VERIFICADOR automático que a aplica
    contra uma promoção real de canal "stable" é trabalho do F9 (catálogo stable/hardening),
    como já estava previsto.
  - platform_backup_records (tabela de evidência de backup ligada ao Update Manager) é F6
    (backend), não F4 — esta fase só garante que a EVIDÊNCIA real (scripts já existentes) exista
    e seja verificável, não a tabela que vai indexá-la.
Próxima fase: F5 — Update Agent resiliente e recovery CLI
```

**Veredito: PASS**

---

## F5 — Update Agent resiliente e recovery CLI

```
Fase: F5
Baseline SHA: 7c677f4 (commit mais recente desta branch antes de F5); HEAD atual 7042b8b, mais
  o trabalho desta sessão (daemon/CLI/agentconfig) ainda staged, não comitado até o fim deste
  update.
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação:
  internal/platformrelease/agent/ledger.go (novo, 3f314b2): estado durável fora do Postgres
    (ADR-0054 §2.3, SQLite via modernc.org/sqlite — pure-Go, CGO_ENABLED=0 compatível) para
    lock/fencing/heartbeat por instalação, tabela update_runs (idempotency_key único por
    instalação) e run_checkpoints (journal append-only). AcquireLock/ReleaseLock com fencing
    token monotônico; TransitionRun/RecordCheckpointStart/RecordCheckpointDone sempre dentro de
    uma transação que valida o fencing ANTES de escrever — nenhuma mutação de estado sobrevive a
    um lock perdido. Schema migrável de forma idempotente e segura sob concorrência real (coluna
    aditiva recovery_acknowledged). AllRuns (nova nesta sessão) lista também runs terminais,
    usada por `updatectl status`/`rollback` para ver histórico sem precisar da API.
  internal/platformrelease/agent/compose.go (novo, 0133130): ComposeAdapter real — pull por
    digest, WriteOverride (arquivo de override determinístico, nunca edita o compose base),
    Apply/WaitHealthy (via `docker compose ps`/healthcheck real, nunca um sleep fixo),
    CurrentSnapshot, Rollback. Nenhuma chamada shell monta comando a partir de string do
    manifesto sem lista de argumentos explícita (nunca shell=true/injeção).
  internal/platformrelease/agent/reconcile.go (novo, 7042b8b; +constructors/exports nesta
    sessão): Executor.Reconcile/Execute — motor idempotente que avança um run do lock até
    SUCCEEDED ou até RECOVERY_REQUIRED (R0–R4, ADR-0054 §2.5) via journal de checkpoints
    (backup → verificação REAL de evidência, nunca só exit-code → download por digest →
    snapshot → migração restrita a Rollout.Workloads/migrations.target → deploy → health →
    observação). `recover()` extraído como método chamável tanto de uma falha recém-ocorrida
    quanto de uma retomada pós-crash (run já em FAILED/RECOVERING ao reiniciar o processo) —
    nunca reverte dados automaticamente, só código/config via Compose.Rollback, e só quando a
    classe de recuperação declarada permite. `Executor.WithLockAndHeartbeat` (extraído nesta
    sessão) isola o padrão lock+heartbeat-em-background+release para ser reusado por
    `updatectl rollback` sem duplicar essa lógica de concorrência. Exportados nesta sessão:
    `NewMigrateRunner`/`NewBackupRunner`/`NewBackupVerifier` (construtores reais, nunca os fakes
    de teste) e as funções puras `LastCompletedDetail`/`SnapshotFromCheckpoints`, para que
    `updatectl` leia o snapshot de um run via ledger sem precisar da API.
  internal/platformrelease/agentconfig/ (novo, nesta sessão): config.go compartilhado pelos dois
    binários — flags com fallback `OMNIRA_AGENT_*` (flag sempre vence env), `Config.Build()`
    monta o Executor real (nunca os fakes de teste), `ReconcileOnce` é o laço
    "ActiveRuns → deserializar manifest_json → Executor.Reconcile" reusado pelo daemon e por
    `updatectl reconcile`.
  apps/update-agent/cmd/omnira-update-agent/main.go (novo, nesta sessão): daemon supervisionado
    — reconcilia imediatamente ao iniciar, depois em loop por `--poll-interval`
    (`runLoop` extraído e testável, nunca reconcilia de novo após o contexto ser cancelado),
    reage a SIGTERM/SIGINT via `signal.NotifyContext`.
  apps/updatectl/cmd/omnira-updatectl/main.go (novo, nesta sessão): CLI local — `status`
    (lista/detalha runs do ledger, nunca precisa da API), `start` (cria um run; exige
    `--approved-out-of-band` — ver "o que ainda não existe" abaixo), `reconcile` (chama
    ReconcileOnce diretamente, útil quando o daemon está fora do ar), `acknowledge-recovery`,
    `rollback` (reinstala o digest anterior compatível — delega inteiramente a
    `Executor.Execute`/`recover()`, nunca uma reimplementação paralela; só aceita runs em
    FAILED/RECOVERING; nunca migra, nunca exige DATABASE_URL).
  Dockerfile.update-agent (novo): build multi-stage reproduzível (CGO_ENABLED=0,
    golang:1.25-alpine) com stage final `export` (`FROM scratch`) contendo só os dois binários —
    nunca uma imagem destinada a rodar como container (o agente precisa do socket Docker do
    HOST via systemd nativo, ADR-0054 §2.1/§2.4). Build real executado e os dois binários
    extraídos/confirmados executáveis (`file`, `--help`) nesta sessão.
  docs/release/systemd/omnira-update-agent.service (novo): unit systemd como ARTEFATO
    VERSIONADO apenas — nunca copiado para /etc/systemd/system/ nem instalado/habilitado por
    esta sessão (exigência explícita do usuário: "nunca instalar nada em produção sem
    autorização explícita"). Documenta as pré-condições operacionais (usuário dedicado sem
    grupo `docker`, EnvironmentFile fora do ExecStart para DATABASE_URL nunca aparecer em
    `ps aux`, sudoers restrito aos comandos exatos invocados) e por que não usa
    `NoNewPrivileges=yes` (quebraria o próprio mecanismo de `sudo -n` pontual que
    `--compose-sudo` depende).
  scripts/release/test-update-agent-cli.sh (novo): orquestra o teste de integração Docker real
    de `apps/updatectl` (registry OCI descartável, duas imagens versionadas com healthcheck
    sensível à versão) — mesmo padrão de scripts/release/test-update-agent-compose.sh.
Arquivos: ver Implementação acima.
Migrations: nenhuma (o ledger do agente é SQLite local, fora do Postgres por desenho — ADR-0054
  §2.3; as tabelas platform_* de auditoria/API continuam reservadas para F6).
Contratos/endpoints: nenhum HTTP (a API de /api/v1/platform/* é F6); `updatectl` fala
  diretamente com o ledger local, nunca com a API, para funcionar quando ela estiver fora do ar.
Gates de segurança / RLS: N/A (sem tabela de plataforma/tenant nesta fase). Gate equivalente
  desta fase: fencing por instalação (nunca por tenant) bloqueia qualquer escrita de um holder
  que perdeu o lock, validado dentro da MESMA transação que a mutação, nunca depois.
Testes executados (reais, nunca mockando o que precisa de infraestrutura real):
  go test ./internal/platformrelease/... ./apps/update-agent/... ./apps/updatectl/... -race →
    PASS (unit, SQLite real em disco, sudo -n real quando disponível — skip gracioso quando não)
  gofmt -l / go build ./... / go vet ./... (repo inteiro) → limpos
  bash scripts/release/test-update-agent-compose.sh (ComposeAdapter, herdado de antes desta
    sessão) → PASS, Postgres/Docker real via registry OCI descartável
  bash scripts/release/test-update-agent-cli.sh (novo) → PASS:
    TestUpdatectl_Integration_RollbackForcesStuckRecoveryDecision — deploya v2 de verdade via
    ComposeAdapter, cria um run FAILED no ledger com snapshot real de v1, roda
    `updatectl rollback --run ... --yes` e confirma o container real volta a v1
    (CurrentSnapshot), run termina ROLLED_BACK.
  sudo -n docker build -f Dockerfile.update-agent --target export -o <dir> . → build real
    confirmado; binários extraídos, `file` confirma ELF estático, `--help`/`-h` executado em
    ambos com sucesso.
  Testes reais de resiliência a crash/restomada (herdados de 7042b8b, cobrindo o requisito da
  especificação "testar power-loss/crash/restart e jobs duplicados"):
    TestExecute_ResumeAfterCrash_NeverRepeatsCompletedSteps,
    TestExecute_ResumeAtObserving_RevalidatesHealthBeforeSucceeding,
    TestExecute_ResumeFromRecovering_CompletesDecision,
    TestExecute_ResumeFromRecovering_StaleFencing_RefusesBeforeTouchingDocker,
    TestExecute_ResumeFromFailed_CompletesDecision (simulam o estado exato pós-crash — retomada
    a partir de QUALQUER checkpoint — sem precisar matar um processo real de verdade);
    TestAcquireLock_OtherHolderWhileValid_Refused, TestTransitionRun_FencingLostBlocksWrite
    (job duplicado/concorrente é recusado pelo fencing, nunca corrompe estado);
    TestConcurrentHeartbeatAndCheckpoints_NoSpuriousErrors,
    TestOpenLedger_ConcurrentMigration_NeverFailsWithDuplicateColumn (concorrência real contra o
    SQLite do ledger, goroutines reais, sem mocks).
  LIMITAÇÃO ACEITA: fault injection via SIGKILL de um processo rodando de fato (vs. simular o
  estado pós-crash via o ledger) não foi construída nesta fase — o custo/risco de orquestrar
  isso de forma controlada não se justificou frente à cobertura equivalente já obtida: todo
  caminho de retomada é determinado inteiramente pelo estado persistido no ledger (checkpoints +
  fencing), nunca por memória de processo, então simular esse estado via teste de unidade
  exercita o MESMO código de decisão que um SIGKILL real exercitaria.
Review Codex (evidência real): `codex exec review --uncommitted` executado repetidamente nesta
  fase, em três etapas sucessivas da mesma disciplina fix→retest→re-review:
    - ledger.go: 2 rodadas até convergência.
    - compose.go: 8 rodadas até convergência.
    - reconcile.go (motor completo): 18 rodadas até convergência.
    - agentconfig/apps/update-agent/apps/updatectl (esta sessão): 14 rodadas até
      "No actionable regressions were identified". Achados reais corrigidos nesta etapa:
        1. [P1] `updatectl rollback` original bypassava isRollbackSafe, heartbeat e
           Rollout.Workloads — reescrito para delegar inteiramente a Executor.Execute/recover(),
           restrito a runs FAILED/RECOVERING.
        2. [P2] `start` tratava qualquer manifesto estruturalmente válido como "já aprovado"
           (nunca há verificação de assinatura real nesta sessão — BLOCKED_EXTERNAL) — corrigido
           com a flag obrigatória `--approved-out-of-band`, um gate honesto, não segurança falsa.
        3. [P2] `--compose-sudo` aplicado também ao MigrateRunner quebraria DATABASE_URL sob
           `env_reset` do sudo — corrigido removendo sudo de migrate (nunca precisa de Docker).
        4. [P2] scripts/backup-omnira-db.sh usa `docker exec` de verdade — backup PRECISA do
           mesmo privilégio Docker que Compose; BACKUP_DIR/DB_NAME transportados como argumentos
           do comando `env` (sobrevivem ao `env_reset` do sudo; `--` removido por incompatibili-
           dade real com o `env` do uutils coreutils deste host).
        5. [P2] PG_CONTAINER/PG_OWNER nunca eram configuráveis nem propagados ao backup runner;
           `--poll-interval<=0` passava validação e crashava o `time.NewTicker` do daemon —
           corrigidos ambos.
        6. [P2] `--lease<1s` permitia outro processo roubar o lock da instalação quase
           imediatamente — `ValidateLease()` adicionada, chamada por `Validate()` e
           explicitamente por `start`/`acknowledge-recovery` (que não passam por `Build()`).
        7. [P1] `reconcile`/`rollback` usavam `context.Background()` — um SIGINT no meio de uma
           migração/backup órfãos os subprocessos privilegiados e parava o heartbeat sem
           liberar/recuperar o lock — corrigido com `signal.NotifyContext` compartilhado.
        8. [P2] Idempotência por `manifest.ReleaseID` fixo impedia retry de uma release que
           terminou ROLLED_BACK/CANCELLED/BLOCKED (ActiveRuns exclui terminais) — corrigido com
           `idempotencyKeyForRetry`, 3 rodadas de refinamento: (a) só olhar a key original
           perdia retries já existentes; (b) casar por PREFIXO de string colidia com
           release_ids textualmente parecidos mas distintos (o regex de release_id permite
           sufixo livre); fixado comparando o `ReleaseID` estruturado deserializado do
           manifest_json de cada run, nunca string matching.
        9. [P2] Tratar SUCCEEDED como "sempre reusar a key" deduplicava PERMANENTEMENTE até uma
           release já bem-sucedida — impedia reaplicar/downgrade de volta para ela depois de uma
           release seguinte. Corrigido: só reusa a key enquanto a tentativa anterior está ATIVA;
           terminal (sucesso OU falha) sempre permite nova tentativa.
       10. [P2] `env`'s last-assignment-wins deixava `b.Env` sobrescrever BACKUP_DIR/DB_NAME
           autoritativos se colidissem por acaso — corrigido filtrando essas chaves de `b.Env`
           antes de apendar os valores autoritativos por último.
       11. [P2] DATABASE_URL registrado como DEFAULT de flag.StringVar vazava a senha do schema
           owner via `--help`/erros de parse (flag.FlagSet grava DefValue e Usage() o imprime
           literalmente) — corrigido: default sempre vazio, fallback do env resolvido
           DEPOIS do parse via `ApplyEnvFallbacks()` (chamado por `Validate()`/`ValidateLease()`).
       12. [P2] `Validate()` exigia DATABASE_URL sempre, bloqueando `rollback` (que nunca migra)
           para um operador de emergência sem credenciais de banco no ambiente — corrigido
           separando `RequireDatabaseURL()`, chamada só por `start`-adjacent/`reconcile`/pelo
           daemon (que podem migrar), nunca por `rollback`.
    Todas as 10 primeiras rodadas desta etapa fecharam com fix→teste real→re-review antes da
    próxima; a 14ª rodada confirmou nenhuma regressão adicional e o resultado foi comitado
    (382da93). Uma 15ª rodada (`codex exec review --commit 382da93`, revisando o commit já
    fechado como confirmação adicional) encontrou mais 3 achados reais, todos no artefato
    systemd/runner de backup (nunca afetam produção — o unit nunca é instalado — mas
    tornariam a instalação documentada literalmente quebrada se um operador a seguisse ao
    pé da letra):
       13. [P2] Os defaults de `--ledger-path`/`--compose-override-dir`
           (`/var/lib/omnira-agent/...`) não batiam com `StateDirectory=omnira-update-agent`
           do unit systemd (que o systemd cria/possui como `/var/lib/omnira-update-agent`) —
           instalado com o EnvironmentFile mínimo documentado, o daemon falharia ao abrir o
           ledger. Corrigido alinhando os defaults ao nome real do binário/serviço.
       14. [P2] Sob `--compose-sudo`/`UMask=0077` do unit, o backup real roda inteiramente
           como root via sudo (que PRESERVA o umask do chamador, nunca reseta) — o dump
           nasceria root:root 0700/0600, ilegível para o próprio agente sem privilégio
           tentar ler depois via BackupVerifier. Corrigido isolando a mudança a um
           subprocesso `sh -c 'umask 0027; exec "$@"'` (nunca ao processo do agente inteiro,
           que teria uma corrida real contra a goroutine de heartbeat).
       15. [P1] A correção do achado 14 (`sudo -g <gid do agente>`) SEM `-u` explícito NUNCA
           eleva o UID — sudo -g troca só o GID efetivo, mantendo o UID do chamador
           (confirmado ao testar: `sudo -n -g "#<gid>" id` devolve uid=<chamador>, não
           uid=0) — quebrando inteiramente o propósito de `--compose-sudo`: o backup real
           (`docker exec`) voltaria a rodar SEM privilégio e falharia antes de criar
           qualquer dump. Corrigido com `-u root` explícito junto de `-g #<gid>`
           (confirmado: `sudo -n -u root -g "#<gid>" id` devolve uid=0 gid=<gid do agente>
           — as duas propriedades exigidas ao mesmo tempo). O teste do achado 14 não pegou
           isso porque o script fake original nunca fazia nada que exigisse root — reescrito
           para gravar o próprio `id -u` visto dentro do dump e falhar se não for 0.
       16. [P2] O Dockerfile exportava os binários sob um `/out/` intermediário
           (`FROM scratch AS export` + `COPY ... /out/<bin>`), enquanto o `ExecStart` do
           unit e o comando de instalação documentado assumiam os binários direto na raiz
           do diretório exportado — seguindo a instalação ao pé da letra, o systemd falharia
           com EXEC até alguém mover os binários manualmente. Corrigido exportando direto na
           raiz (`COPY ... /<bin>`); reconfirmado com um `docker build --target export -o
           <dir> .` real: os binários aparecem em `<dir>/<bin>`, sem subdiretório.
    Uma 16ª rodada (`--uncommitted`, após corrigir 13-16) confirmou que o achado 15 era real
    e crítico; o fix do achado 15 foi testado e re-revisado numa 17ª rodada, que fechou com
    "No actionable regressions were identified".
Commits: 3f314b2 (ledger), 0133130 (compose), 7042b8b (reconcile), 382da93 (daemon/CLI/
  agentconfig); os achados 13–16 (systemd/backup runner) aguardam o commit seguinte (ver git
  log da branch para o SHA final).
Pendências/risco:
  - BLOCKED_EXTERNAL (sem credenciais nesta sessão): verificação real de assinatura/proveniência
    (GHCR/Cosign, release/policies/signing.yaml) não existe — `domain.ParseManifest` valida só a
    FORMA do manifesto. `--approved-out-of-band` em `updatectl start` é um gate operacional
    honesto para essa lacuna, nunca uma simulação de segurança que não existe de fato.
  - `apps/updatectl`'s integração ponta-a-ponta não pôde exercitar `start`→`reconcile` reais
    contra um release de verdade: `domain.ParseManifest` exige imagens `ghcr.io/...@sha256:...`,
    que nunca resolvem contra um registry OCI local descartável sem acesso real ao GHCR
    (BLOCKED_EXTERNAL). O teste de integração construído (`TestUpdatectl_Integration_
    RollbackForcesStuckRecoveryDecision`) cobre o que É testável sem essa credencial: a parte de
    `rollback` que só lê o ledger e usa Compose contra imagens locais reais.
  - systemd unit entregue só como artefato (docs/release/systemd/omnira-update-agent.service) —
    nunca instalado; usuário/grupo dedicado, EnvironmentFile e sudoers restrito continuam sendo
    passos manuais do operador, fora do escopo de código deste programa.
  - Fault injection real via SIGKILL de processo (vs. a cobertura equivalente via estado
    persistido no ledger) não foi construída — ver "LIMITAÇÃO ACEITA" em Testes executados acima.
Próxima fase: F6 — Backend e autorização global (release service, planner, preflight, update
  runs/event store, platform admin/update operator como permissão própria, nunca membership de
  tenant/Hub como autoridade).
```

**Veredito: PASS**


## F6 — Backend e autorização global

```
Fase: F6
Baseline SHA: e0e560e (último commit de F5 nesta mesma branch)
Branch/worktree: feat/omnira-release-update-engine @ /opt/omnira/worktrees/release-engineering
Implementação:
  internal/platformrelease/updatemgr (novo, 2 arquivos de produção): o "Update Manager API" do
    ADR-0054 §2.1 — release service, planner/preflight, update runs/event store e a comunicação
    autenticada com o Update Agent (F5). Autoridade é SEMPRE IsPlatformOperator
    (internal/hub/provisioning, ADR-0038), nunca tenant_id/membership de tenant/Hub/API Headless
    — mesmo modelo de confiança de internal/hub/companies: o serviço roda numa sessão de SISTEMA,
    re-autorizando o operador explicitamente em toda chamada (nunca confiando em RLS como única
    barreira), e a defesa em profundidade é dupla: a sessão de RLS do PRÓPRIO chamador (HTTP,
    exige tenancyadapters.UserSessionMiddleware) E a sessão de sistema do Service.
  service.go (1494 linhas): IngestRelease (valida com domain.ParseManifest/Validate de F1 +
    SignatureVerifier — ver achados de segurança abaixo — e idempotência por release_id E por
    Idempotency-Key), GetInstallation/SetInstallationVersion (singleton platform_installations,
    idempotente), RecordBackupEvidence (bookkeeping de release/policies/backup.yaml, idempotente),
    CreatePlan (preflight fail-closed: checkCompatibility contra compatibility.yaml, evidência de
    backup como WARN informativo — o gate bloqueante é F9 — idempotente), StartRun ("comunicação
    autenticada com o agente": reserva no Postgres, despacha para o MESMO ledger SQLite local que
    apps/update-agent/apps/updatectl já usam — AcquireLock -> CreateRun -> ReleaseLock, nunca um
    caminho HTTP/Docker separado — e só então confirma LOCKED no espelho), GetRun/ListRuns/
    ListEvents (sincronizam com o ledger antes de responder). Toda mutação exige Idempotency-Key
    (ADR-0054 §2.2.1).
  http.go (411 linhas): HTTPHandler com 404 uniforme em negação (nunca 403, mesmo padrão de
    internal/hub/adapters.AdminHandler), rejeita tenant_id/hub_id do payload (CLAUDE.md),
    paginação por cursor (Cursor de internal/platform/pagination), rate limit por usuário
    (ratelimit.EnforceUser).
  internal/platform/httpserver/server.go: RegisterPlatformReleaseHandlers (novo), 10 rotas
    /api/v1/platform/* atrás de OMNIRA_PLATFORM_RELEASE_API_ENABLED.
  internal/platform/config/config.go, apps/api/cmd/omnira-api/main.go: novos campos de
    configuração (PlatformReleaseAPIEnabled + os MESMOS OMNIRA_AGENT_LEDGER_PATH/
    INSTALLATION_ID/LEASE do Update Agent — nunca uma configuração separada, ver ADR-0054 §2.4).
  internal/platform/httpserver/contract_test.go: newRoutedServer registra o novo handler;
    TestEveryDocumentedOperationIsARegisteredRoute confirma as 10 rotas documentadas.
  contracts/openapi/omnira-v1.yaml: tag PlatformRelease + 10 operações (releases, installation,
    installation/version, backups, updates/plans, updates/plans/{id}, updates/runs,
    updates/runs/{id}, updates/runs/{id}/events) + 7 schemas novos (ReleaseManifest,
    PlatformRelease, PlatformInstallation, PlatformBackupRecord, PlatformUpdatePlan,
    PlatformUpdateRun, PlatformUpdateEvent).
Arquivos:
  migrations/000173_platform_release_tables.{up,down}.sql (novo)
  internal/platformrelease/updatemgr/{service,http}.go (novo)
  internal/platformrelease/updatemgr/{world_test,service_integration_test}.go (novo, 1230 linhas
    de teste de integração real contra Postgres + SQLite)
  internal/platform/httpserver/server.go, internal/platform/httpserver/contract_test.go
  internal/platform/config/config.go, apps/api/cmd/omnira-api/main.go
  contracts/openapi/omnira-v1.yaml
Migrations: 000173_platform_release_tables — 12 tabelas GLOBAIS (sem tenant_id; a varredura de
  RLS por tenant_id de internal/platform/db/rls_completeness_test.go não as alcança, RLS é
  aplicada manualmente aqui): platform_installations (singleton via PK booleana com
  CHECK(singleton) — nunca depende de disciplina de aplicação), platform_releases,
  platform_update_plans, platform_update_runs, platform_update_events (append-only de verdade:
  REVOKE UPDATE/DELETE + trigger, mesmo padrão de conversation_closures/migration 000087), e 6
  tabelas de idempotência (platform_release_ingest_requests, platform_update_plan_requests,
  platform_update_run_requests, platform_backup_record_requests,
  platform_installation_version_requests) + platform_backup_records + platform_component_versions
  (schema criado para concordar com o ADR; população real é pendência documentada abaixo). Todas
  com FORCE ROW LEVEL SECURITY e policy restrita a is_system_admin() OR
  is_platform_operator(current_user_id()) — mesmo padrão de platform_operators (migration 000099).
  lint-migration-safety.sh (F4) e validate-migrations.sh (F2) re-executados: OK (161 migrations).
Contratos/endpoints: 10 operações novas em /api/v1/platform/* (ver Implementação); validate-specs.sh
  (F2, redocly lint) OK; TestEveryDocumentedOperationIsARegisteredRoute (drift guard de F0)
  confirma rota real para cada uma.
Testes executados (comandos, PASS/FAIL):
  gofmt -l . / go build ./... / go vet ./... (repo inteiro) → limpos
  go test ./internal/platform/httpserver/... ./internal/platformrelease/... -race → PASS
  scripts/release/test-integration-hostrun (variante local do scripts/test-integration.sh do F2
    rodando `go test` no HOST em vez de containerizado — achado F4 pré-existente, não desta fase:
    um worktree Git secundário monta `.git` com caminho absoluto do host, que o container não vê;
    rodar no host contorna isso sem mudar o script versionado) → 20 funções de teste em
    internal/platformrelease/updatemgr, todas PASS, contra Postgres 16 descartável real E o
    ledger SQLite real do Update Agent (nunca mockados): autorização cross-identidade (operador
    de plataforma passa; tenant admin, delegado de Hub, "CRM externo"/API Headless sem
    authn.Principal, operador revogado e um usuário qualquer são todos recusados com o mesmo
    ErrForbidden/404 uniforme — teste dedicado TestUpdateMgr_AuthorizationBoundary, mais
    TestUpdateMgr_TheServiceRefusesOnItsOwn provando que o Service recusa por conta própria
    mesmo sem a checagem HTTP); idempotência dedicada para as 5 mutações (IngestRelease,
    CreatePlan, StartRun, RecordBackupEvidence, SetInstallationVersion — replay devolve o mesmo
    resultado sem duplicar, mesma chave com conteúdo diferente é 422); preflight fail-closed
    (versão desconhecida bloqueia por padrão; incompatibilidade de versão/protocolo de agente
    bloqueia); StartRun escrevendo de verdade no MESMO ledger SQLite (LOCKED, evento run.locked
    append-only); sincronização bidirecional run-ledger-Postgres incluindo runs criados por
    `updatectl` fora da API (sem nenhuma linha em Postgres) e recuperação manual
    (SetInstallationVersion) vencendo um histórico de ledger mais antigo; bloqueio de novo
    despacho enquanto há RECOVERY_REQUIRED não reconhecido (mesmo gate que Executor.Reconcile).
  go test ./internal/hub/adapters/... ./internal/hub/provisioning/... ./internal/platform/db/...
    (regressão ampliada, mesma variante host) → internal/hub/provisioning e internal/platform/db
    PASS (confirma que as 12 tabelas novas e a RLS/policies adicionadas não quebraram nenhum
    pacote existente de autorização/RLS). internal/hub/adapters: FAIL em 2 subtestes de
    TestDelegatedRealtimeStreams ("a_contract_limited_to_some_queues..." e
    "with_a_limited_contract_the_visibility_answer_is_reused_only_briefly...", ambos
    serving_realtime_integration_test.go, falha "first event = \"\", want the one of the allowed
    queue"/"inside the scope: \"\""). INVESTIGADO E CONFIRMADO PRÉ-EXISTENTE, NÃO É REGRESSÃO
    DESTA FASE: reexecutado isoladamente duas vezes nesta branch (determinístico, mesmos 2
    subtestes) E uma terceira vez em worktree temporário no commit BASE e0e560e (último commit de
    F5, HEAD antes de qualquer alteração de F6) — falha idêntica, nos mesmos 2 subtestes, sem
    nenhuma linha deste programa aplicada. Nada em F6 toca
    internal/hub/adapters/serving_realtime*.go, autenticação delegada de Hub ou NATS/SSE; a causa
    é sensibilidade a timing deste teste específico neste ambiente de execução (não investigada a
    fundo — fora do escopo de correção desta fase), não as tabelas/migrations/handlers adicionados
    aqui. Documentado para o dono do repositório decidir se investiga a parte; worktree temporário
    de comparação removido (git worktree remove) após a verificação, nada residual.
Gates de segurança / RLS: 12 tabelas novas, todas FORCE RLS + policy restrita a
  is_platform_operator() (ver Migrations acima). Defesa em profundidade dupla em toda rota HTTP
  (sessão de RLS do chamador + sessão de sistema do Service, cada uma recusando
  independentemente — TestUpdateMgr_TheServiceRefusesOnItsOwn prova a segunda mesmo simulando a
  primeira ausente). tenant_id/hub_id no payload são rejeitados explicitamente (400), nunca usados
  como autoridade. SignatureVerifier: forma válida (domain.Validate) nunca é autenticidade — o
  default de PRODUÇÃO (blockedExternalVerifier) recusa TODA ingestão até um verificador real
  existir (BLOCKED_EXTERNAL, ver Pendências); os testes injetam um verificador de teste nomeado
  para nunca ser confundido com produção.
Review Codex (evidência real): `codex exec review --uncommitted` executado 21 vezes nesta
  branch (mesmo ciclo rigoroso fix→teste real→re-review das fases anteriores — nunca aceitar um
  achado sem corrigir e testar contra Postgres/SQLite reais antes da próxima rodada). As duas
  últimas rodadas (20ª e 21ª) convergiram em "No actionable defects/regressions were
  identified/confirmed". Mais de 20 achados reais corrigidos, agrupados por tema:
  - **Identidade de run do ledger descartada**: Ledger.CreateRun pode devolver um run EXISTENTE
    (dedup por installation_id+idempotency_key) cujo RunID diverge do proposto; o chamador
    precisa usar o valor real, nunca descartá-lo.
  - **Reserva antes do despacho (crash-safety)**: um run tinha que existir no Postgres (estado
    sentinela REQUESTED, nunca um domain.RunState) ANTES do despacho real ao ledger — um crash
    entre os dois deixava o pedido visível e auditável, nunca perdido silenciosamente.
  - **Preflight fail-open em versão desconhecida**: uma instalação recém-migrada (current_version
    NULL) era tratada como "compatível com qualquer release" — corrigido para BLOCK por padrão;
    SetInstallationVersion é o único jeito de saí-lo (endpoint novo, idempotente).
  - **Revalidação na janela de validade do plano/despacho**: um plano PASS cacheado, ou um pedido
    reservado (REQUESTED) nunca despachado, precisavam revalidar compatibilidade contra o estado
    ATUAL (nunca o momento da criação), inclusive depois de um crash/retry.
  - **Autenticidade do manifesto**: forma válida nunca é autenticidade — SignatureVerifier
    (default: bloqueia tudo, nunca finge verificar) fecha o gap até F9 ter credenciais reais.
  - **Só um run ativo por instalação**: duas Idempotency-Keys diferentes podiam ser despachadas
    em sequência, a segunda nunca revalidada contra o que a primeira já tinha mudado — corrigido
    com lock de instalação (nunca só por chave) + contagem Postgres + consulta DIRETA ao ledger
    (pega runs criados por `updatectl`, invisíveis ao Postgres).
  - **Corridas de sincronização run-ledger-Postgres**: refresh dentro da MESMA transação que podia
    rejeitar o pedido (desfazia o próprio refresh); refresh concorrente podia retroceder um
    estado mais novo para um mais antigo (serializado pelo mesmo lock de instalação); o timestamp
    de sincronização usava now() do MOMENTO DA SINCRONIZAÇÃO em vez do timestamp do PRÓPRIO run
    do ledger, misturando dois relógios e descartando sucessos reais permanentemente;
    SetInstallationVersion usava now() da transação (que no Postgres é o INÍCIO da transação,
    não a instrução) em vez de clock_timestamp().
  - **Recuperação manual (ADR-0054 §8 R3/R4) perdendo para histórico de ledger obsoleto**: depois
    de um run SUCCEEDED seguido de uma restauração manual aprovada, o despacho seguinte não podia
    ficar presos para sempre atrás do histórico antigo do ledger — decide pelo fato mais RECENTE
    (ledger vs. Postgres) por timestamp, nunca um sempre vencendo o outro por tipo.
  - **RECOVERY_REQUIRED não reconhecido**: o mesmo gate que Executor.Reconcile usa
    (HasUnresolvedRecoveryRequired) precisa bloquear CreatePlan/StartRun, senão um pedido é
    aceito mas o agente real nunca o processa — rechecado inclusive sob o lock do ledger.
  - **Idempotência das 5 mutações**: IngestRelease, CreatePlan, StartRun, RecordBackupEvidence e
    SetInstallationVersion precisavam, cada uma, da MESMA disciplina (Idempotency-Key obrigatória,
    hash do pedido, replay vs. 422) — corrigidas uma a uma conforme encontradas.
  - **Corridas de concorrência em condições de borda**: ingestão concorrente do mesmo release_id
    inexistente (UNIQUE constraint como 500 genérico em vez de replay/422); um retry concorrente
    de StartRun podia sobrescrever um run JÁ despachado/concluído com COMPATIBILITY_VOIDED (void
    agora exige state=REQUESTED, nunca incondicional).
  LIMITAÇÃO ACEITA, NÃO CORRIGÍVEL SEM TRANSAÇÕES DISTRIBUÍDAS: Postgres (platform_installations)
  e o ledger SQLite local são dois sistemas de armazenamento diferentes; a atomicidade entre eles
  é best-effort por desenho (ADR-0054 §2.4: "nunca duas fontes de verdade concorrentes para o que
  fazer agora" — o ledger decide, Postgres espelha). writeLedgerRun fecha a janela prática ao
  manter o lock de instalação do Postgres detido durante TODA a sequência (leitura + lock do
  ledger + CreateRun), mas uma janela de nanossegundos entre a escrita no SQLite e o commit do
  Postgres sempre existirá em sistemas sem 2PC — aceito e documentado, nunca escondido.
Commits: (ver git log da branch feat/omnira-release-update-engine — commit desta fase citado no
  próximo update deste arquivo)
Pendências/risco:
  - Assinatura real de release (GitHub Artifact Attestation / Sigstore keyless, ver
    release/policies/signing.yaml) continua BLOCKED_EXTERNAL (gh CLI/credenciais GHCR ausentes,
    achado original F0 §7) — IngestRelease está corretamente fail-closed (SignatureVerifier
    default recusa tudo) até um verificador real ser instalado via WithVerifier (F9).
  - platform_component_versions: schema criado (ADR-0054 §2.4), população real a partir de um
    deploy efetivo é pendência — nada neste programa ainda relata "o que está instalado em cada
    build_unit" além de current_version/current_release_id (que já são sincronizados de verdade
    a partir do ledger e de `updatectl`).
  - Evento NATS job.platform.update.apply.v1 citado no ADR-0054 §2.2.2 como mecanismo alternativo
    de comunicação com o agente NÃO foi implementado — StartRun comunica com o agente escrevendo
    DIRETO no ledger SQLite local compartilhado (o mesmo mecanismo que apps/updatectl já usa),
    que já satisfaz a regra rígida "API nunca executa shell/Docker, só enfileira um pedido
    autorizado" sem precisar de um segundo transporte; documentado como decisão arquitetural
    desta fase, não um gap.
  - Nenhuma UI consome esta API ainda (F7).
  - SSE não implementado para ListEvents — polling por cursor `after` (seq) cobre a cláusula
    "SSE/polling" da especificação; documentado como pendência de UX, não de corretude.
  - A migração de `scripts/test-integration.sh` para rodar `go test` no host em vez de em
    container (necessário para esta fase rodar de verdade, achado PRÉ-EXISTENTE do F4) não foi
    aplicada ao script VERSIONADO nesta sessão — usei uma variante local descartável para
    verificação; recomendado ao dono do repositório decidir a estratégia de mount definitiva
    (fora do escopo de correção de F6).
  - internal/hub/adapters.TestDelegatedRealtimeStreams: 2 subtestes falham por sensibilidade a
    timing NESTE ambiente de execução — PRÉ-EXISTENTE, confirmado reproduzindo a falha idêntica
    no commit base e0e560e (antes de qualquer linha de F6), ver "Testes executados" acima para o
    detalhe da investigação. Não é um gate de F6 (nenhum arquivo tocado por esta fase participa
    do caminho de streaming delegado), mas fica registrado para quem for investigar a causa raiz
    do timing, possivelmente em fase futura ou fora deste programa.
Próxima fase: F7 — Frontend real do OMNIRA (menu "Administração global → Plataforma → Versões e
  Atualizações", React/Vite existente, consumindo as 10 rotas desta fase)
```

**Veredito: PASS**

---
