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
  - `tools/validate-schema.sh` e `tools/check-rls.sh` existentes apontam, por padrão, para
    `omnira-postgres`/`omnira_dev` (o banco real) — não foram usados nestes workflows; os gates
    novos usam exclusivamente containers descartáveis (scripts/test-integration.sh).
  - Drift pré-existente classificado, não corrigido (fora de escopo — "não refatore partes não
    relacionadas"): 42 arquivos Go sem gofmt, 47 problemas de ESLint (4 erros), 19 achados do
    govulncheck, 16 vulnerabilidades de dependências npm, 35 erros/7 warnings de redocly (este
    último já baselined via .redocly.lint-ignore.yaml; os outros quatro não têm mecanismo de
    ratchet ainda — recomendado como item de limpeza separado ao dono do repositório).
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
