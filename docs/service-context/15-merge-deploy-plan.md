# 15 — Plano de merge e implantação (para executar só com ordem do dono)

Estado de referência (2026-10-15): branch `feat/omnira-service-context-operations` sobre `main` @ `26e4f01` (= o que roda em produção: api+worker+web `sha-26e4f01`, ledger de migrations em 137). `main` está 25 commits à frente de `origin/main` (nunca empurrado). **Nada deste plano foi executado.** Este documento complementa o `12-deployment-rollback.md` (ordem de flags, rollback) com a parte de git e de operação.

## 0. Regras para não atrapalhar outras atividades

- Executar numa janela em que nenhuma outra sessão toque o checkout principal (`20-OMNIRA`): ele é **compartilhado** e outra sessão já trocou a branch dele no meio de um deploy (2026-10-07). Antes de **cada** passo: `git branch --show-current` deve imprimir `main` e `git status --porcelain` deve vir vazio.
- O `docker compose` do projeto vive no checkout principal (nomes de projeto/volumes dependem do diretório). Não rodar `compose` a partir de worktree.
- O serviço `migrate` monta `./migrations` **vivo**: só rodá-lo com o checkout principal limpo e já no commit a implantar.
- Só api, worker e web são recriados. Postgres, NATS, Valkey, WAHA e Keycloak **não** reiniciam. Os containers `omnira-hl-*` (stack do headless) não são tocados.
- Sem push, sem tag. Push é uma etapa separada (§6) com ordem própria.

## 1. Antes de começar (somente leitura)

```bash
cd /data/home-moved/Projects/_legacy_lowercase_projects/20-OMNIRA
git branch --show-current            # main
git status --porcelain               # vazio
git rev-parse --short main           # esperado 26e4f01 (se mudou, ver §1.1)
git worktree list                    # ver se há outra sessão ativa
docker ps --format '{{.Names}} {{.Status}}' | grep -E 'omnira-(api|worker|web|postgres)'
docker exec omnira-postgres psql -U omnira -d omnira_dev -Atc "select max(version) from schema_migrations"   # esperado 000137_queue_capacity_no_limit
git merge-tree --write-tree main feat/omnira-service-context-operations >/dev/null && echo SEM_CONFLITO
```

### 1.1 Se `main` andou desde 26e4f01

O merge limpo deixa de ser prova do gate. Rebasear não é necessário: fazer o merge, rodar o gate completo da §2 na árvore resultante (não só os testes do módulo) e só então seguir. Se houver migration nova em `main` com número entre 150 e 159, parar e renumerar as minhas (reservei 150–159).

## 2. Merge local (sem push)

Merge com commit (`--no-ff`), como o do Flow Builder (`5d3b8e8`): uma linha só para reverter (`git revert -m 1`).

```bash
git merge --no-ff feat/omnira-service-context-operations \
  -m "merge: Service Context & Operations (ADR-0049) - flags off, migrations 150-156"
git diff --stat 26e4f01 HEAD | tail -1       # 131 arquivos aprox.
```

Como `main` não andou, a árvore do merge é idêntica à da branch: os resultados do gate da branch valem. Mesmo assim, **provar na árvore final**:

```bash
go build ./... && go vet ./...                       # via docker golang:1.25 (host sem go)
scripts/test-integration.sh ./internal/servicectx/... ./internal/routing/... ./internal/hub/... ./internal/flows/... ./internal/attendance/...   # args separados
(cd web && npm ci && npm test -- --run && npx tsc --noEmit)
```

Esperado: tudo verde exceto as 8 falhas que também falham em `main` (outbox/adapters ×5, `TestPlatformOperator_GrantRevokeAndVisibility`, `TestQueriesStayFastOnABusyConversation`, `TestStandardSectorsMigrationAddsTheThreeSectorsAndRespectsWhatExists`). Qualquer falha **nova** = parar e reportar.

Round-trip das migrations num banco descartável: `up` 138→156, `down` 156→150, `up` de novo.

## 3. Implantação (api + worker + web juntos)

O worker sobe junto com a api (regra da capacidade 0 e hooks). Receita já usada em produção, a partir do checkout principal limpo em `main`:

```bash
SHA=$(git rev-parse --short HEAD); TS=$(date +%Y%m%d-%H%M)

# 3.1 backup + tags de rollback (imagens atuais)
scripts/backup-omnira-db.sh
for i in api worker web; do docker tag 20-omnira-$i:latest 20-omnira-$i:rollback-pre-svcctx-$TS; done

# 3.2 .env: nenhuma variável nova é obrigatória (todas as flags nascem false).
#     Conferir que NÃO há OMNIRA_SERVICE_* ligada por engano:
grep -n 'OMNIRA_SERVICE_' .env || echo "nenhuma (ok)"

# 3.3 migrations 150-156 (aditivas; owner; lock_timeout 5s em cada arquivo)
docker compose up migrate
docker exec omnira-postgres psql -U omnira -d omnira_dev -Atc "select max(version) from schema_migrations"   # 000156_service_issue_protocol
docker exec omnira-postgres psql -U omnira -d omnira_dev -Atc "select has_table_privilege('omnira_app','service_sessions','SELECT'), has_function_privilege('omnira_app','service_issue_protocol(uuid,text)','EXECUTE')"   # t|t

# 3.4 imagens e recriação (infra intocada)
docker compose build api worker web
for i in api worker web; do docker tag 20-omnira-$i:latest 20-omnira-$i:sha-$SHA; done
docker compose up -d --no-deps --force-recreate api worker web
for s in omnira-api omnira-worker omnira-web; do docker inspect -f '{{.Name}} {{.State.Health.Status}}' $s; done
docker inspect -f '{{.Created}}' omnira-api    # tem que ser de agora (nunca só "healthy")
curl -sf -o /dev/null https://omnira.devops.k3gsolutions.com.br/ && echo web_ok
```

Atalho equivalente: `scripts/deploy.sh` (exige árvore limpa, `npm ci` + testes web, build, `compose up migrate`, recreate, health e smoke públicos). Preferir o passo a passo acima quando o motivo for controlar as tags de rollback.

**Ordem é obrigatória:** migrations (3.3) **antes** do binário novo. Com flags off o binário novo não toca nas tabelas novas; ainda assim a 000156 tem que existir antes de qualquer flag.

### 3.5 Verificação pós-deploy com flags off

- `/healthz` e `/metrics` ok; `omnira_service_*` aparecem zerados ou ausentes (módulo desligado).
- Uma conversa de teste: receber, assumir, responder, finalizar. Comportamento igual ao de antes.
- `docker logs omnira-api --since 5m | grep -i 'service-context'` vazio.

## 4. Ligar o módulo por etapas (cada uma = editar `.env` + `docker compose up -d --no-deps --force-recreate api worker`)

Só em empresa de teste primeiro (a flag é global; combinar quem usa o sistema na hora). Depois de cada etapa, rodar o smoke do `12` e observar `/metrics` por alguns minutos.

| Etapa | Variável | Também precisa |
|---|---|---|
| a | `OMNIRA_SERVICE_CONTEXT_ENABLED=true` | api **e** worker recriados; conceder `service.*` a papéis personalizados (decisão do dono) |
| b | `OMNIRA_SERVICE_FINALIZE_HOOK_ENABLED=true` | etapa a |
| b2 | `OMNIRA_SERVICE_AUTO_OPEN_ON_CLAIM_ENABLED=true` | etapa a; só api importa. Esta variável **não** chegava ao container antes do commit que acompanha este documento (faltava no `docker-compose.yml`) |
| c | `OMNIRA_SERVICE_EXTERNAL_ORDERS_ENABLED` | **não ligar**: não há adaptador real de ERP (responderia 503) |

Antes da etapa b2, o administrador pode escolher o prefixo (`PUT /service-settings/protocol-prefix`, ainda sem tela); senão sai da derivação do nome da empresa e fica congelado no primeiro protocolo. Conferir antes: `GET /service-settings/protocol-prefix` da empresa.

## 5. Rollback

| Situação | Ação |
|---|---|
| Comportamento estranho com flag ligada | voltar a flag para `false`, recriar api+worker (dados ficam) |
| Binário ruim | `docker image inspect` da tag `rollback-pre-svcctx-<TS>`; `docker tag … :latest`; `docker compose up -d --no-deps --force-recreate api worker web`. As migrations são aditivas e o binário antigo as ignora |
| Schema | **roll-forward** (política do `PILOT-RUNBOOK`). `down` 156→150 apaga sessões/eventos/tarefas novos; só se ainda não houver dados que importem, e nunca como rollback de rotina |
| Merge no git (local) | antes do push: `git reset --hard 26e4f01` apenas se o dono pedir; depois do push: `git revert -m 1 <merge>` |

## 6. Push (etapa separada, só com ordem explícita)

`main` está 25 commits à frente de `origin/main`, então o push leva também o trabalho de Pessoas. Comando que funciona neste host (o remote é https sem credencial; o ssh do host força a porta 51212):

```bash
GIT_SSH_COMMAND='ssh -i ~/.ssh/id_ed25519_github -o IdentitiesOnly=yes -o BatchMode=yes -o Port=22' \
  git push git@github.com:keslleykledston/20-OMNIRA.git main
```

Sem tag. Registrar o deploy em `docs/operations` somente se o dono pedir.

## 7. Depois do merge: outras branches

- `feat/headless-crm-dual-operation` (18 commits, worktree `/data/cafegpt/tmp/wt-headless`): rebasear sobre o novo `main`. Conflitos de texto esperados em `apps/api/cmd/omnira-api/main.go`, `apps/worker/cmd/omnira-worker/main.go`, `docker-compose.yml`, `internal/platform/config/config.go`, `internal/platform/httpserver/server.go`, `internal/routing/application/assign.go`, `internal/attendance/application/service.go`, `internal/flows/adapters/effects_postgres.go`, `internal/flows/ports/runtime.go`, `internal/iam3/security_test.go`, `internal/tenancy/adapters/team_http_test.go`, `web/src/lib/permissions.ts`. Renumerar as migrations dela (137–141 colidem com o 000137 de `main`); para os eventos `service_*` de webhook, estender os CHECKs de tipo/escopo (ver `06`).
- Demais worktrees (`wt-admin`, `wt-brand`, `wt-idle`, `wt-opennotice`, `wt-sched`) já estão em dia com `main` e não compartilham arquivos com este trabalho.

## 8. Pendências que não bloqueiam o merge, mas valem antes de ligar flags

- Codex: a última correção (round 10) não passou por um round 11.
- Decisão do dono sobre R-AI-1 (rota de ferramentas de IA aceita `proposed_by` do cliente; herdado do ADR-0017).
- Quem recebe `service.*` nos papéis personalizados; `service.request_external`.
- Tela para o administrador escolher o prefixo; a rota antiga `/hubs/{hub}/inbox/{item}/claim` não dispara a abertura automática.
- Smoke em número de teste (WhatsApp) e e2e: não executados.

## 9. Estado em 2026-10-17

Executado e implantado em `main` (origin `1f3713a` + commits locais posteriores): merge do módulo, flags (a), (b) e (b2) ligadas, migrations 150–158. **Branch do headless preparada, não mesclada:** `prep/headless-on-main` (worktree `/data/cafegpt/tmp/wt-headless-prep`) é o merge de `feat/headless-crm-dual-operation` sobre `main` com 4 conflitos de texto resolvidos por união e as migrations 137–141 renumeradas para **159–163**; `go build`/`vet` limpos, pacotes tocados ok, web 1080/1080. A branch original está intacta. Falta, **depois** de o dono mandar mesclar: os 20 nomes públicos `service_*` nos CHECKs do diário e dos webhooks (gate 4 de `06`).
