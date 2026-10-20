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
