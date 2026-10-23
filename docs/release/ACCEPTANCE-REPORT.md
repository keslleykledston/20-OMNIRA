# ACCEPTANCE-REPORT — OMNIRA Release Engineering (F0–F11)

Fase: F11 — Aceitação e entrega.
Spec: `docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md` (§F0–F11, §12, §13, §15).
Branch/worktree: `feat/omnira-release-update-engine` @ `/opt/omnira/worktrees/release-engineering`.
Baseline (merge-base com `main`): `748528a`.
HEAD nesta aceitação: `4ca0ce1` (92 commits acima do baseline, todos locais — **nada enviado**,
nenhum `git push`, nenhum PR aberto de fato).
Data da bateria final: 2026-10-23.

Este relatório consolida evidência **reexecutada nesta sessão** (não apenas citada de memória de
fases anteriores). Onde um número já estava documentado numa fase anterior e não foi
reexecutado agora, isso é dito explicitamente.

## 1. Matriz de fases (F0–F11)

| Fase | Veredito | Commits | Rodadas Codex | Achados reais corrigidos |
|---|---|---|---|---|
| F0 — Auditoria e baseline | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F1 — ADRs/schemas/design | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F2 — CI completo | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F3 — Builder/cadeia de suprimentos | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F4 — Compatibilidade DB/backup | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F5 — Update Agent/CLI | PASS | ver IMPLEMENTATION-STATUS.md | — | — |
| F6 — Backend/autorização | PASS | `a91b1f3`, `6e8cf28` | 21 | 20 |
| F7 — Frontend real | PASS | `b8976be` (+ anteriores) | 18 | 20 |
| F8 — Staging/fault injection | PASS (com ressalva — ver §4) | `0ae4e0c`..`28233d3` | 15 | 22 |
| F9 — Hardening/revisão independente | PASS | `150f79d`..`25f0ebc` | 20 | 20+ |
| F10 — Operacionalização/docs | PASS | `8df3123`..`baa3234` | 23 | 14–15 |
| F11 — Aceitação e entrega | PASS (2 BLOCKED_EXTERNAL documentados) | `3adbca7`, `4ca0ce1`, + este commit | 4 (2 fatias) | 2 |

(F0–F5 datalham commits/rodadas em `docs/release/IMPLEMENTATION-STATUS.md`, não repetidos aqui
palavra por palavra para não divergir da fonte única.)

## 2. Bateria final reexecutada nesta sessão (F11)

### 2.1 Build

- `go build ./...` — limpo, sem erro. Evidência: saída vazia (sucesso).

### 2.2 Unit + integração Go (sem Postgres real)

- `go test ./...` — **108 pacotes `ok`, 0 `FAIL`**. Evidência: `/tmp/f11-go-test.log` (local,
  não commitado — efêmero desta sessão).
- Achado positivo nesta sessão: `sudo -n` está disponível neste host (diferente do registrado em
  F5/F10), então os dois testes antes pulados por falta de sudo real rodaram DE VERDADE e
  passaram: `TestExecBackupRunner_EnvSurvivesSudoEnvReset`,
  `TestExecBackupRunner_SudoBackup_RunsAsRootAndStaysReadableByCaller`
  (`internal/platformrelease/agent`). **Ressalva importante**: este `sudo` é amplo
  (`ALL:ALL NOPASSWD:ALL`, confirmado via `sudo -n -l`) — um sandbox de desenvolvimento, não a
  regra de sudoers ESTREITA documentada no runbook de instalação para o Update Agent em
  produção. Isto prova que o CÓDIGO do backup-runner funciona sob sudo real; não prova a regra
  de sudoers mínima em si, que continua exigindo validação por um operador com
  `sudo -n -l` no host de produção real (já assim documentado em F10).
- `internal/platformrelease/agent/compose_integration_test.go` (precisa de Docker real
  roteirizado via `OMNIRA_COMPOSE_INTEGRATION_TEST=1`) — SKIP, como esperado (flag não setada
  nesta execução padrão).

### 2.3 RLS/PostgreSQL real (achado + corrigido nesta sessão)

`scripts/test-integration.sh` (suíte canônica de integração real-Postgres do repo, pré-existente)
falhava ao rodar a partir deste worktree — `.git` de um worktree é um arquivo apontando para um
caminho ABSOLUTO fora do diretório montado no container de teste, então a descoberta de
repositório do git falhava ("fatal: not a git repository") antes mesmo do `go test` começar.
Corrigido em `scripts/test-integration.sh` (commit `3adbca7`): monta também o `git-common-dir`
real (resolvido com `git rev-parse --git-common-dir` + `realpath`) no mesmo caminho absoluto;
checkout normal (não-worktree) fica inalterado porque o common-dir já está dentro da raiz
montada. 2 rodadas Codex limpas; validado rodando de verdade.

Reexecutado contra um Postgres 16 descartável real (nunca `omnira-postgres`/`omnira_dev`):

| Pacote | Resultado | Evidência |
|---|---|---|
| `internal/platformrelease/updatemgr` | PASS | `ok ... 12.974s` — 136 tabelas com FORCE RLS confirmadas pelo próprio `migrate-sql verify`; `omnira_app` sem bypass (`rolsuper=f, rolbypassrls=f`) |
| `internal/platformrelease/agent` | PASS | `ok ... 7.915s` |
| `internal/tenancy/adapters` | PASS | `ok ... 92.100s` — Tenant A/B, acesso cruzado recusado, membership revogada |
| `internal/hub/adapters` | 1 falha (`TestDelegatedRealtimeStreams`) | reproduz a MESMA falha pré-existente e não relacionada já documentada em F6 (confirmada por A/B contra o baseline antes de qualquer código deste programa); não é regressão deste programa |

### 2.4 Web (Vitest)

`npm run test -- --run` (suíte completa, 117 arquivos, 1208 testes): **1189 passaram, 19
falharam** na primeira execução, sob contenção de recursos (117 arquivos em paralelo neste
sandbox). Investigação:
- `PlatformReleasesPage.test.tsx` (código deste programa, F7): 28/28 passam em isolamento.
- `TicketPanel`, `AttendancePages`, `PeoplePage`, `ContactsPage`: 100% passam em isolamento —
  flakiness de contenção de recursos, não ligada a este programa (nenhum desses arquivos foi
  tocado por F0–F11).
- `FlowEditorPage.test.tsx`: falha mesmo isolado, com
  `TypeError: RequestInit: Expected signal ("AbortSignal {}") to be an instance of AbortSignal`
  — incompatibilidade `jsdom@30`/`undici` nativo do Node, já presente em `main` antes deste
  programa (confirmado: `jsdom": "^30.1.0"` já existe em `main`); pré-existente, não é regressão.
- Nenhuma falha sobrevive em isolamento dentro do código deste programa.

### 2.5 Contratos

- `tools/validate-specs.sh` — OpenAPI 3.0.0, lint (redocly), AsyncAPI 2.6, `docs/API.md`,
  `docs/EVENTS.md`: todos **OK**.

### 2.6 Staging/fault injection (F8) — reexecutado, achado real corrigido, limitação nova documentada

Reexecução de `scripts/release/test-f8-staging.sh` (primeira desde que F9/F10 passaram a existir)
encontrou um achado real de sequenciamento: o seed de migrations do staging ainda parava em
`000173` e usava sua própria marker de teste coincidentemente numerada `000174`, colidindo com
as migrations REAIS `000174`/`000175` que F9 acrescentou ao repositório — a API real quebrava
imediatamente (`relation "platform_release_revocations" does not exist`). Corrigido no commit
`4ca0ce1`: seed real até `000175`, marker de teste renumerada para `000176`. 2 rodadas Codex
limpas; reexecutado de verdade:

| Cenário | Resultado pós-correção | RTO |
|---|---|---|
| 0 — pull rejeitado pelo ghcr.io real | PASS | 3–4s (ordem de grandeza do já documentado) |
| 1 — baseline v1 (deploy real, migration `000176` genuinamente pendente aplicada pelo agente) | PASS (SUCCEEDED, marker confirmada no Postgres real) | 117.58s |
| 2 — health check nunca passa (rollback automático R1) | **BLOCKED_EXTERNAL** (ver detalhe abaixo) | — |
| 3 — mesma falha + R3 declarado | bloqueado em cascata pelo mesmo motivo do Cenário 2 | — |
| 4 — kill -9 durante apply | não alcançado (depende de reparo prévio bloqueado pelo Cenário 3) | — |
| 5 — migration drift (checksum_bundle) | não alcançado nesta reexecução (mesma cascata); lógica equivalente cobertura por unit tests (ver abaixo) | — |

**Limitação nova, documentada, não corrigível sem comprometer um controle de segurança ou tocar
configuração do host (ambos fora do alcance desta sessão)**: F9 acrescentou
`rollbackTargetReleaseIDs` (`internal/platformrelease/agent/reconcile.go`), que reparseia o
`ManifestJSON` de runs ANTERIORES via `domain.ParseManifest` para confirmar a proveniência de um
rollback automático. `domain.ParseManifest` exige literalmente o host `ghcr.io/...` — mas os
manifestos que `scripts/release/f8harness` usa (para testar sem credenciais GHCR reais) usam o
registry local descartável e, por desenho documentado desde F8, NUNCA passaram por esse parser
na criação. A partir do Cenário 2 (primeiro que decide um rollback automático com um run
SUCCEEDED anterior já no ledger), o re-parse falha e a instalação cai em
`RECOVERY_REQUIRED`/`LOCKED` em vez de `ROLLED_BACK`, bloqueando 2/3/4/5 em cascata.
**Não é regressão de produção**: todo manifesto real já foi `domain.ParseManifest`-validado na
ingestão (API `IngestRelease` ou `updatectl start`), então o re-parse em produção sempre
sucede. A LÓGICA de `rollbackTargetReleaseIDs` permanece coberta por 6 testes dedicados reais
(ledger real, sem mock) em `internal/platformrelease/agent/reconcile_f9_test.go`
(`TestExecute_RollbackTarget_*`), **reconfirmados PASS nesta sessão**. Resolver de verdade
exigiria credenciais de publish no GHCR real do projeto (BLOCKED_EXTERNAL, mesma causa raiz de
todo o programa) ou alterar DNS/daemon Docker do host (proibido — afetaria a stack real em
produção neste mesmo host). Documentado em `docs/release/IMPLEMENTATION-STATUS.md` (seção F8,
Pendências) e aqui — nunca simulado como se tivesse passado.

### 2.7 Segurança (GitHub Actions)

Auditoria completa já feita em F9 (SSRF, path traversal, injection, segredo em logs,
autenticação, driver de deploy) sem achados novos. Reconfirmado nesta sessão que
`.github/workflows/*.yml` não mudou desde o commit da auditoria de F9 (`0bb7df5`) — nenhuma
reauditoria completa repetida, apenas confirmação de que o escopo já auditado continua
inalterado.

## 3. BLOCKED_EXTERNAL consolidado (programa inteiro)

Todos têm a MESMA causa raiz desde F0: esta sessão não tem `gh` CLI, credenciais GHCR nem
infraestrutura Cosign/Sigstore configuradas, e não pode criar um GitHub Environment real
(interface web, fora do alcance de um agente de CLI).

1. **Publicação real de imagens no GHCR e assinatura/attestation (Cosign)** — todo o
   enforcement LOCAL do que essas credenciais gateariam (verificação de assinatura, revogação,
   anti-replay, canais) foi implementado e testado de verdade com um verificador de teste
   nomeado explicitamente; só a geração real da assinatura/publicação fica pendente.
2. **GitHub Environment com reviewers obrigatórios para aprovação de produção** — os workflows
   referenciam o nome do Environment; criar o Environment em si é uma ação de interface web do
   GitHub que exige um administrador do repositório real.
3. **Inspeção ao vivo de execuções do CI (aba Actions/Security)** — sem `gh` CLI/credenciais,
   replicado localmente onde possível (`tools/validate-specs.sh`, testes dos próprios
   workflows), documentado como BLOCKED_EXTERNAL onde não.
4. **Assinatura real da lista de revogação** — mesma causa raiz de (1); o mecanismo de
   consumo/enforcement da revogação já está implementado e testado.
5. **Regra de sudoers mínima e exata do backup em produção** — o runbook documenta o comando
   exato; esta sessão só pôde validar o CÓDIGO sob um sudo amplo de sandbox (ver §2.2), nunca a
   regra restrita real — um operador de produção precisa validar com `sudo -n -l`.
6. **F8 Cenários 2–5 ponta a ponta pós-F9** — ver §2.6; não exige credencial nenhuma para ser
   resolvido de verdade (exigiria DNS/daemon Docker do host OU as mesmas credenciais GHCR de
   (1)), por isso listado aqui por completude em vez de arquivado como "não é nada".

Nenhum destes itens bloqueia o enforcement/teste LOCAL do que cada um gateria — só a
ativação/publicação externa em si.

## 4. Limites de rollback conhecidos (consolidado)

- Rollback automático (R1/R2) só reverte CÓDIGO (digest), nunca dado — `*.down.sql` nunca
  autoriza rollback de dado; `database_strategy=destructive`/`recovery_class=R3` força
  `RECOVERY_REQUIRED` manual sempre.
- RPO real = tempo desde o último `pg_dump` lógico (backup não-PITR; WAL archiving/PITR não
  instalado neste host — achado F0, fora do escopo deste programa). RTO medido em F8: ~6,4–8,1s
  para um restore comprovado como `omnira_app` pós-restore (F4); RTOs do fluxo completo de
  deploy+rollback medidos em F8 na faixa de 100–150s (ver tabela em IMPLEMENTATION-STATUS.md,
  seção F8).
- Revogação força `RECOVERY_REQUIRED` incondicionalmente mesmo quando a release se declara
  "segura de reverter" — nunca um "forward-fix automático" escondido como rollback normal.
- GC de catálogo nunca apaga a release `current_release_id` de nenhuma instalação nem uma
  release referenciada por um run local não-terminal.

## 5. Runbooks (entregues em F10, revalidados por referência aqui)

`docs/release/runbooks/`: `01-primeira-instalacao-agente.md`, `02-release-e-promocao.md`,
`03-rollback-e-recovery.md`, `04-restore-e-pitr.md`, `05-ci-ghcr.md`,
`06-aprovacao-producao.md`, `07-revogacao.md`, `08-troubleshooting.md` (+ `00-indice.md`). Todo
comando documentado neles foi executado de verdade em F10 (ver IMPLEMENTATION-STATUS.md, seção
F10) — não reexecutados individualmente aqui, só referenciados.

## 6. Estado do repositório nesta aceitação

- Branch: `feat/omnira-release-update-engine`. Worktree: limpo (`git status --short` vazio após
  o commit `4ca0ce1`).
- 92 commits acima de `main` (baseline `748528a`), **nenhum `git push`**, **nenhum PR aberto**.
- `docs/release/PR-DESCRIPTION-DRAFT.md` (este mesmo commit) contém o texto pronto para abertura
  de PR quando o usuário autorizar — não enviado.
- Nenhuma instalação em produção foi tocada, criada ou modificada por este programa. Nenhum
  systemd unit do Update Agent foi instalado fora deste worktree de desenvolvimento.

## 7. Veredito final do programa (F0–F11)

**PASS**, com os itens de §3 permanecendo BLOCKED_EXTERNAL por falta de credenciais/infra fora
do alcance desta sessão — cada um documentado com o passo exato que um operador autorizado
precisa executar (nos runbooks correspondentes), nunca fingido como feito. Entrega = capacidade
operacional implementada e validada em ambiente seguro (testes reais, Postgres real, Docker
real, revisão independente Codex convergida em todas as fases), pronta para homologação/ativação
controlada por esse operador — nunca produção já atualizada por esta mudança.
