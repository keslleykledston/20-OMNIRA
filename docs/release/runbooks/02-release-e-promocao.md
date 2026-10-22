# Release e promoção

Cobre: construir um manifesto de release, promovê-lo entre canais (`dev` → `candidate` → `stable`, `release/policies/channels.yaml`), e aplicá-lo numa instalação real.

**Convenção deste runbook:** os comandos de `updatectl` abaixo (seções 3 e 4) usam o binário **já instalado** pelo bootstrap (`01-primeira-instalacao-agente.md`), nunca `go run` — o host real de uma instalação não tem (e não deve precisar de) um toolchain Go, cache de build gravável pela identidade de serviço, ou acesso de rede para baixar dependências, especialmente numa recuperação de emergência:
```sh
UPDATECTL=/opt/omnira/update-agent/bin/omnira-updatectl
```
Só substitua por `go run ./apps/updatectl/cmd/omnira-updatectl` numa checkout de desenvolvimento/staging com o toolchain Go disponível, nunca na instalação real.

## 1. Construir e validar o manifesto

```sh
go run ./apps/releasectl/cmd/releasectl build-manifest \
  --release-id omnira-2.4.0 --platform-version 2.4.0 --channel candidate \
  --repository keslleykledston/20-OMNIRA --commit-sha <sha do commit> --workflow-run-id <run id do CI> \
  --artifact omnira-api=ghcr.io/<repo>/omnira-api@sha256:<digest real> \
  --module platform_release --from-version 2.3.0 --from-version 2.3.1 --min-agent-protocol 1 \
  --database-strategy expand_then_contract_later \
  --migrations-start-after 000168 --migrations-target 000172 --migrations-checksum sha256:<checksum real> \
  --rollout-workload omnira-api --recovery-class R1 --approved-by release-policy-candidate-v1 \
  --out manifest.json

go run ./apps/releasectl/cmd/releasectl validate-manifest --file manifest.json
# ou: make release-validate-manifest MANIFEST=manifest.json
```
(`-h` em `build-manifest` lista todos os flags; repita `--artifact`/`--module`/`--rollout-workload`/`--from-version` para cada unidade/versão suportada da release. `compatibility.from_versions` é comparado por igualdade exata (`slices.Contains`, `internal/platformrelease/updatemgr/service.go`) — nunca um padrão/wildcard; declare cada versão concreta que de fato pode fazer upgrade direto para esta release.)

`validate-manifest` valida **só a forma** do manifesto (`internal/platformrelease/domain.ParseManifest`): campos obrigatórios, `release_id` conforme `releaseIDRe`, `artifacts[].image` como referência `ghcr.io/...` por digest `sha256:...` (nunca tag móvel — confirmado rejeitando `release/fixtures/manifest-tampered-movable-tag.json` com exit code 1). **Isto não é verificação de assinatura, proveniência ou revogação** — essa verificação real é `BLOCKED_EXTERNAL` nesta instalação (sem `gh` CLI/credenciais GHCR/Cosign confirmadas; ver `release/policies/signing.yaml` e `05-ci-ghcr.md`).

## 2. Canais e promoção (`release/policies/channels.yaml`)

| Canal | Instalável em | Exige staging (F8) | Exige aprovação de platform_operator |
|---|---|---|---|
| `dev` | ambiente de dev/lab isolado | não | não |
| `candidate` | staging | não | não |
| `stable` | staging e produção | **sim** | **sim** |

Promoção a `stable` exige: todos os gates G0-G10 da especificação verdes, evidência de backup/restore testado dentro de `promotion_gate.stable_channel.max_evidence_age_hours` (24h, `release/policies/backup.yaml`), e aprovação explícita de um `platform_operator` no momento da instalação (promover o **catálogo** nunca dispara instalação automática em nenhuma instalação real — ver `06-aprovacao-producao.md`).

## 3. Aplicar numa instalação (hoje: via `updatectl`, backend F6 quando a API estiver habilitada)

Com a Update Manager API desabilitada (`OMNIRA_PLATFORM_RELEASE_API_ENABLED=false`, default), o único ponto de entrada para criar um run é `updatectl start`.

**Carregue o mesmo `EnvironmentFile`/diretório do daemon ANTES de `start`, não só antes de `reconcile`** — se esta instalação definir `OMNIRA_AGENT_LEDGER_PATH` (ou qualquer outra flag) diferente do default, `start` sem esse ambiente carregado grava o run num ledger **diferente** do que o daemon está observando (silenciosamente: a criação "funciona", mas o run nunca é reconciliado):

```sh
cd /opt/omnira/20-OMNIRA   # MESMO WorkingDirectory do unit systemd
set -a; source /etc/omnira/update-agent.env; set +a   # MESMO EnvironmentFile do daemon — ledger-path/installation-id etc.
$UPDATECTL start \
  --manifest=/path/to/manifest.json \
  --approved-out-of-band
```
(`--installation-id` já vem de `OMNIRA_AGENT_INSTALLATION_ID` no `EnvironmentFile` — repita `--installation-id=<id>` explicitamente só se estiver operando fora desse ambiente, ex. testando contra um ledger de dev.)

`--approved-out-of-band` é **obrigatória e nunca tem default `true`**: como a verificação real de assinatura está `BLOCKED_EXTERNAL`, esta flag é a confirmação explícita do operador de que revisou o manifesto por outro meio antes de usar o comando — nunca finja que substitui a verificação real quando ela existir.

`start` grava um run em estado `LOCKED` no ledger local e retorna o `run_id`. Dali, ou o daemon `omnira-update-agent` (se estiver rodando — ver `01-primeira-instalacao-agente.md`) reconcilia automaticamente a cada `--poll-interval` (default 15s), ou você dispara manualmente:

```sh
cd /opt/omnira/20-OMNIRA   # MESMO WorkingDirectory do unit systemd — compose/migrate usam caminhos RELATIVOS
set -a; source /etc/omnira/update-agent.env; set +a   # MESMO EnvironmentFile do daemon — compose-base-files/compose-sudo/DATABASE_URL etc.
$UPDATECTL reconcile --installation-id=<id>
```

Carregue sempre o **mesmo** `EnvironmentFile` que o daemon usa (`01-primeira-instalacao-agente.md`) antes de qualquer `reconcile`/`rollback` manual — a CLI interativa não herda o `EnvironmentFile` do systemd por si só; sem isto, a execução manual usaria os defaults de `agentconfig.FlagSet` (ex. `--compose-base-files=docker-compose.yml:docker-compose.override.yml`, `--compose-sudo=false`), que podem **divergir** da configuração real desta instalação (ver o aviso em `01-primeira-instalacao-agente.md` sobre nunca assumir uma lista de compose files genérica), e a reconciliação manual pode falhar ou mutar o stack errado. Rode como a mesma identidade que tem acesso ao ledger (`omnira-update-agent` ou um operador com `sudo` para esse usuário), nunca com credenciais pessoais soltas.

`reconcile` (e qualquer caminho que possa migrar até `MIGRATING_EXPAND`) **exige** `OMNIRA_AGENT_DATABASE_URL`/`DATABASE_URL` (ou `--database-url`) — `rollback` não exige, porque nunca migra (ver `03-rollback-e-recovery.md`). **Use sempre a variável de ambiente, nunca `--database-url` na linha de comando**: diferente do valor de ambiente (lido só depois do parse, nunca impresso em `--help`/erros), um argumento de linha de comando fica visível em `ps aux`/histórico do shell/logs de auditoria do processo — o mesmo cuidado que `docs/release/systemd/omnira-update-agent.service` já aplica via `EnvironmentFile`.

## 4. Acompanhar

Mesmo ambiente carregado (acima) — `status` lê o mesmo `--ledger-path` configurado, nunca o default, se este diferir:

```sh
$UPDATECTL status
$UPDATECTL status --run=<run_id>   # journal de checkpoints
```

Com a Update Manager API habilitada, o mesmo histórico (e o fluxo de monitoramento, `08-troubleshooting.md`) está disponível em `GET /api/v1/platform/updates/runs`, `GET /api/v1/platform/updates/runs/{run_id}`, `GET /api/v1/platform/updates/runs/{run_id}/events` — sempre lendo o mesmo ledger local, nunca uma segunda fonte de verdade.

## 5. Retentativa após falha

Reenviar `start --manifest <mesmo manifesto>` para uma release cujo run anterior **já terminou** (`ROLLED_BACK`, `RECOVERY_REQUIRED`, `CANCELLED`, `BLOCKED`) cria uma **nova tentativa genuína**, nunca retorna o run antigo (a idempotency key é derivada do estado real do run anterior, não só do `release_id` — ver comentário de `idempotencyKeyForRetry` em `apps/updatectl/cmd/omnira-updatectl/main.go`). Enquanto o run anterior ainda está **ativo**, uma chamada repetida é deduplicada para o mesmo run (proteção contra duplo clique).
