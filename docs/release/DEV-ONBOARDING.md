# Dev onboarding — OMNIRA Release Engineering

Como rodar e explorar localmente o sistema de release/update implementado em F0–F9 (`internal/platformrelease/`, `apps/update-agent`, `apps/updatectl`, `apps/releasectl`, `scripts/release/`), sem tocar nenhuma instalação real. Para operar uma instalação de verdade, use `docs/release/runbooks/` — este documento é só para desenvolvimento/exploração.

## 1. Testes unitários/integração (rápidos, sem Docker)

```sh
go test ./internal/platformrelease/...
# ou: make update-test
```

Os testes de integração de `internal/platformrelease/updatemgr` (`service_integration_test.go`) exigem Postgres real (`OMNIRA_DATABASE_URL`/`OMNIRA_APP_DATABASE_URL`) e são **pulados silenciosamente** (`SKIP`) sem essas variáveis — isto é esperado em CI sem banco, nunca um sinal de que passaram. Suba o Postgres do stack de dev (`make docker-up`) e exporte as duas URLs reais antes de rodar, se quiser esses testes executando de verdade.

## 2. `updatectl` contra um ledger local descartável

Nunca aponte `--ledger-path`/`OMNIRA_AGENT_LEDGER_PATH` para `/var/lib/omnira-update-agent/` numa máquina de dev — use um arquivo descartável:

```sh
LEDGER=/tmp/dev-ledger-$$.db
go run ./apps/updatectl/cmd/omnira-updatectl status --installation-id=dev --ledger-path="$LEDGER"
```

`status` sobre um ledger novo/vazio é sempre leitura pura e segura. `start`/`reconcile`/`rollback` já mutam esse ledger local (nunca nada fora dele sem `--database-url`/Docker real) — seguro de experimentar repetidamente com um `$LEDGER` descartável.

## 3. Construir e validar um manifesto de teste

```sh
go run ./apps/releasectl/cmd/releasectl validate-manifest --file release/fixtures/manifest-good.json   # válido, exit 0
go run ./apps/releasectl/cmd/releasectl validate-manifest --file release/fixtures/manifest-tampered-movable-tag.json   # rejeitado, exit 1
```

`release/fixtures/` tem outros manifestos inválidos de propósito (`manifest-malicious-*.json`) — úteis para explorar o que `domain.ParseManifest` rejeita e por quê, sem precisar construir um manifesto do zero (ver `02-release-e-promocao.md` para o `build-manifest` completo).

## 4. Suíte de staging real (F8) — o teste mais pesado, mas o único ponta a ponta

```sh
make update-test-staging
# equivalente a: bash scripts/release/test-f8-staging.sh
```

Requer: Docker com `sudo -n docker` funcionando para o usuário que roda o script (o script usa `sudo -n docker` diretamente — confirme com `sudo -n docker ps` antes de rodar), portas livres, e ~10-15 minutos (builda 4 imagens de teste + os binários reais `omnira-api`/`omnira-update-agent`/`omnira-updatectl`/`f8harness`). **Nunca toca a stack real** (`omnira`/`omnira-hl`) — toda a topologia (registry, Postgres, NATS, projeto Compose) é nomeada por PID e descartada ao final.

O script sempre roda a sequência completa (cenário 0 pelo `updatectl` real + cenários 1-5 via `f8harness` — golden path, health check quebrado, incompatibilidade de banco declarada, `kill -9` do agente em pleno apply, migration drift); não há seleção de um subconjunto por argumento. Para desenvolver/depurar um cenário específico sem esperar a sequência inteira, comente temporariamente as seções `section "Cenário N..."` anteriores no próprio script enquanto trabalha localmente — nunca comite o script alterado assim.

Evidência (`report.md`/`report-final.md`, logs por cenário, e o ledger SQLite com seus sidecars `-wal`/`-shm`) sobrevive **incondicionalmente** em `/tmp/omnira-f8-reports/$RUN`, mesmo numa rodada que falha — a função `cleanup()` persiste isso antes de qualquer remoção. **Containers, rede e as 4 imagens de teste são removidos sempre**, com ou sem `KEEP_WORK` — a flag controla só o diretório de trabalho (`$WORK`, os binários compilados desta rodada), nunca a topologia Docker; não há hoje um jeito suportado de pular a limpeza de containers para inspecionar a topologia ainda rodando.

## 5. `f8harness` é staging-only — nunca para produção

`scripts/release/f8harness/main.go` desserializa manifestos sem passar por `domain.ParseManifest` (só para contornar, em staging, a exigência real de `ghcr.io/...` que um registry local descartável não satisfaz) e reusa o **mesmo** `Executor`/`agentconfig.Build()` dos binários reais — nunca reimplementa `reconcile`/`compose`. Não há cmd de produção que o referencie; não o copie para nenhum fluxo fora de `test-f8-staging.sh`.

## 6. Onde ler mais

- `docs/release/IMPLEMENTATION-STATUS.md` — o que cada fase (F0-F9) implementou, com rodadas de revisão Codex e achados reais corrigidos.
- `docs/release/spec/OMNIRA_RELEASE_ENGINEERING_PACOTE_EXECUCAO.md` — a especificação normativa completa.
- `release/policies/*.yaml` — as políticas declarativas (canais, assinatura/revogação, backup/RPO/RTO, compatibilidade) que o código implementa.
- `docs/release/runbooks/` — procedimentos para uma instalação REAL (nunca use estes comandos de produção contra o ambiente de dev, e vice-versa).
