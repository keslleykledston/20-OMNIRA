# Rollback e recuperação sem API

Cobre: forçar o rollback de uma release travada, e operar uma instalação quando a Update Manager API (backend F6) está fora do ar ou nunca existiu (modo standalone) — especificação §8 "CLI local segura para observar status/reconciliar/reinstalar digest anterior compatível quando a API estiver indisponível".

`updatectl` opera **inteiramente sobre o ledger SQLite local e o Docker real desta instalação** — nenhuma chamada de rede, nenhuma dependência do backend/API estar disponível.

**Convenção deste runbook (mesma de `02-release-e-promocao.md`):** `$UPDATECTL` é o binário já instalado (`/opt/omnira/update-agent/bin/omnira-updatectl`, ver `01-primeira-instalacao-agente.md`) — nunca `go run` numa instalação real, especialmente numa recuperação de emergência onde o host pode não ter Go instalado.

## 1. Rollback manual (`updatectl rollback`)

Use quando um run está em `FAILED` ou `RECOVERING` e precisa que a decisão de recuperação seja forçada manualmente (o daemon não está rodando, ou não consegue decidir).

**Antes de rodar de uma shell SSH nova:** carregue o mesmo `EnvironmentFile` do daemon (`/etc/omnira/update-agent.env`, ver `01-primeira-instalacao-agente.md`) e use a identidade de serviço com acesso ao ledger. Sem isto, `agentconfig.FlagSet` usa os defaults (que podem divergir da configuração real desta instalação — ver o aviso em `01-primeira-instalacao-agente.md` sobre compose files) — um rollback de emergência pode falhar ou tocar o stack errado:

```sh
UPDATECTL=/opt/omnira/update-agent/bin/omnira-updatectl
cd /opt/omnira/20-OMNIRA   # MESMO WorkingDirectory do unit systemd — compose/migrate/backup usam caminhos RELATIVOS
set -a; source /etc/omnira/update-agent.env; set +a
$UPDATECTL rollback --installation-id=<id> [--run=<run_id>] [--yes]
```

Sem `--run`, usa o run mais recente desta instalação. Sem `--yes`, mostra o snapshot que seria reinstalado (unit → digest) e pede confirmação interativa (`s/N`).

`rollback` delega para a **mesma** `Executor.Execute`/`recover()` que o daemon usaria automaticamente — nunca uma lógica paralela. Isso garante: (1) o manifesto é lido e `database_strategy`/`recovery_class` são respeitados (nunca um downgrade inseguro para uma release R3/R4 ou com schema incompatível); (2) heartbeat durante a operação (o daemon não pode assumir a instalação concorrentemente); (3) o estado/journal do run é atualizado (a próxima reconciliação automática não retoma o mesmo run do ponto congelado); (4) as units tocadas são derivadas do `Rollout.Workloads` real da release.

`rollback` **nunca migra dados** — só reverte código/config via Compose (snapshot de `PREPARING`). Por isso não exige `--database-url`: um operador de emergência sem acesso às credenciais do schema owner ainda consegue rodar isto.

Resultado possível:
- `ROLLED_BACK`: rollback aplicado e confirmado saudável.
- `RECOVERY_REQUIRED`: rollback automático **não é seguro** para este run — preserve evidências e siga `04-restore-e-pitr.md`; depois de resolver manualmente, use `acknowledge-recovery` (abaixo) antes de qualquer novo run.

Um `--run` que pertence a **outra** instalação é rejeitado (nunca aplica o snapshot errado no projeto Compose configurado).

## 2. Reconciliação manual sem esperar o daemon

```sh
cd /opt/omnira/20-OMNIRA   # MESMO WorkingDirectory do unit systemd — compose/migrate usam caminhos RELATIVOS
set -a; source /etc/omnira/update-agent.env; set +a   # nunca --database-url na linha de comando (ver 02-release-e-promocao.md)
$UPDATECTL reconcile --installation-id=<id>
```
Dispara uma reconciliação imediata de todos os runs ativos desta instalação — útil se o daemon estiver parado mas um run já estiver `LOCKED`/em progresso e você quiser avançá-lo sem subir o systemd unit completo.

## 3. `acknowledge-recovery`: destravar novos runs depois de um `RECOVERY_REQUIRED`

Enquanto houver um `RECOVERY_REQUIRED` não reconhecido, **nenhum run novo é processado** nesta instalação (`status` avisa isso explicitamente). Depois de investigar e resolver manualmente (ver `04-restore-e-pitr.md`):

```sh
$UPDATECTL acknowledge-recovery --installation-id=<id> --run=<run_id>
```

Isto **não desfaz nada** — é só a confirmação, fora de banda, de que um humano investigou e resolveu. Nunca é um substituto para o restore/validação reais.

## 4. Modo standalone: operar sem nunca ter uma Update Manager API

Se esta instalação **nunca** terá a Update Manager API sincronizando revogações (ex.: instalação isolada, sem backend F6 habilitado), marque explicitamente:

```sh
export OMNIRA_AGENT_STANDALONE=true   # ou --standalone em cada comando
```

**Por que isto é explícito e nunca inferido:** o ledger tem um gate de frescor de revogação fail-closed por padrão (`Ledger.RevocationSyncFresh`) — sem ele, uma instalação que simplesmente nunca sincronizou (porque a API está fora do ar há dias) seria indistinguível de uma instalação deliberadamente standalone, e o gate teria que escolher entre "confiar cegamente" (inseguro) ou "nunca bloquear nada standalone de verdade" (também inseguro). A marca `standalone` é gravada por `agentconfig.Config.Build()` — o único ponto que monta um `Executor`/`Ledger` reais tanto para `apps/update-agent` (daemon) quanto para `apps/updatectl` (CLI, sem daemon algum) — então vale igualmente para os dois, de forma idempotente.

**Risco real de habilitar isto numa instalação que na verdade é gerenciada:** revogação de releases nunca será sincronizada com o Postgres enquanto a flag estiver ativa — uma release revogada depois de aplicada aqui não será automaticamente bloqueada de rodar de novo (ex. num rollback futuro) sem a sincronização. Só habilite se tiver certeza operacional de que esta instalação é de fato isolada.

**Voltar ao modo gerenciado:** suba o agente/CLI sem `--standalone`/`OMNIRA_AGENT_STANDALONE` — `Build()` limpa a marca residual automaticamente (achado Codex F9: o caminho inverso precisa ser igualmente explícito; sem isto, uma instalação que já rodou standalone ficaria "confiável" indefinidamente mesmo depois de a API voltar ao ar, caso a primeira sincronização real demorasse).

## 5. Recuperação sem a API (resumo do que funciona sem rede)

| Operação | Precisa da API? | Precisa de `--database-url`? |
|---|---|---|
| `status` | não | não |
| `start` | não (grava no ledger local) | não |
| `reconcile` | não | sim (pode migrar até `MIGRATING_EXPAND`) |
| `rollback` | não | não (nunca migra) |
| `acknowledge-recovery` | não | não |

Nenhum destes comandos depende do backend/API/Postgres `platform_*` estarem disponíveis — a especificação §2.2 exige explicitamente que o agente resista à indisponibilidade da API e do banco.
