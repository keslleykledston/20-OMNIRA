# Primeira instalação do Update Agent (bootstrap)

Instala o daemon `omnira-update-agent` numa instalação que nunca o teve rodando. **Nenhum passo abaixo é executado automaticamente por este programa** — é uma ativação explícita do operador humano, fora do escopo de código deste release engineering (ver cabeçalho de `docs/release/systemd/omnira-update-agent.service`).

## Pré-condições

1. Usuário/grupo de serviço dedicado, sem privilégio de root e **sem** pertencer ao grupo `docker` (equivale a root sobre o host via o socket Docker):
   ```sh
   useradd --system --no-create-home --shell /usr/sbin/nologin omnira-update-agent
   ```
2. Binários extraídos do build reproduzível (nunca compilados ad-hoc no host de produção — ver `05-ci-ghcr.md` sobre o estado real do build/push):
   ```sh
   docker build -f Dockerfile.update-agent --target export -o /opt/omnira/update-agent/bin .
   ```
3. `sudoers` restrito para o usuário de serviço — **nunca** `ALL=(ALL) NOPASSWD: ALL`. Com `--compose-sudo`/`OMNIRA_AGENT_COMPOSE_SUDO=true`, há **duas formas diferentes** de invocação que precisam de regras **separadas** (confirmado lendo `internal/platformrelease/agent/compose.go` e `reconcile.go` — não assuma que uma regra cobre as duas):
   - **Compose/Docker** (`ComposeAdapter`, `internal/platformrelease/agent/compose.go`): argumentos em slice, nunca `sh -c` — mas são **cinco** invocações distintas, não só `docker compose up`: `docker ps -a`, `docker inspect --format ... <id>`, `docker pull <ref por digest>`, `docker compose -p <project> ... config --images` (dry-run de auditoria) e `docker compose -p <project> ... up -d --no-deps <units>`. Autorizar só `docker exec`/`docker ps` (ou só `docker compose up`) deixa `pull`/`inspect`/`config --images` sem permissão — a primeira atualização real falharia num passo diferente do que se testou manualmente.
   - **Backup** (`execBackupRunner`, usado por `tools/migrate-sql.sh`'s pré-requisito de evidência e por qualquer run que mute dados): **não** é um `docker exec` direto — é `sudo -n -u root -g #<gid do agente> sh -c 'umask 0027; exec "$@"' sh env BACKUP_DIR=... DB_NAME=... PG_CONTAINER=... PG_OWNER=... bash scripts/backup-omnira-db.sh` (o `-g #<gid>` faz os arquivos do dump nascerem legíveis pelo próprio agente sem privilégio; o `sh -c 'umask ...'` ajusta a permissão de criação). Uma regra de sudoers que só autoriza `docker exec`/`docker ps` **nunca** autoriza este comando — a primeira atualização que precisar de backup falharia. Escreva (e teste de verdade, `sudo -n -l` como o usuário de serviço) uma regra separada para esta forma exata de invocação antes de confiar nela em produção.
   ```sh
   visudo -cf /etc/sudoers.d/omnira-update-agent
   ```
4. `EnvironmentFile` em `/etc/omnira/update-agent.env` (`root:omnira-update-agent`, modo `0640`) — **nunca** no `ExecStart`/linha de comando (visível via `ps aux`/`/proc/<pid>/cmdline` para qualquer usuário do host). Conteúdo mínimo (lista completa de `OMNIRA_AGENT_*` em `internal/platformrelease/agentconfig/config.go`, e um exemplo comentado sem segredos em `docs/release/examples/update-agent.env.example`):
   ```sh
   OMNIRA_AGENT_INSTALLATION_ID=<id real da instalação>
   OMNIRA_AGENT_DATABASE_URL=postgres://<schema owner, nunca omnira_app>@...
   OMNIRA_AGENT_COMPOSE_BASE_FILES=<a MESMA lista ordenada de compose files que este host usa para subir o stack real>
   OMNIRA_AGENT_COMPOSE_SUDO=true
   ```
   **Nunca copie `docker-compose.yml:docker-compose.prod.yml` como exemplo genérico.** Neste host, `docker-compose.prod.yml` define `OMNIRA_AUTH_MODE=mock` (só para lab) e `docker-compose.override.yml` é quem de fato fornece o bind real da API (`127.0.0.1:28080`) e os mounts de mídia (`/opt/omnira-media`) desta instalação. Usar a lista errada faz o agente aplicar/reverter releases contra uma configuração de autenticação/rede/mídia **diferente** da que está rodando de verdade — identifique e preserve exatamente os compose files (e a ordem) que este host usa hoje antes de preencher esta variável.
   Se esta instalação nunca terá a Update Manager API sincronizando revogações, adicione `OMNIRA_AGENT_STANDALONE=true` — ver a nota de risco em `03-rollback-e-recovery.md` antes de decidir isto.

## Instalação

```sh
cp docs/release/systemd/omnira-update-agent.service /etc/systemd/system/
systemctl daemon-reload
systemctl enable --now omnira-update-agent
```

`StateDirectory=omnira-update-agent` faz o systemd criar `/var/lib/omnira-update-agent` com modo `0700` e dono `omnira-update-agent` automaticamente — não crie este diretório manualmente antes.

## Verificação pós-instalação

```sh
systemctl status omnira-update-agent
journalctl -u omnira-update-agent -n 50 --no-pager
```

A primeira linha de log esperada é `omnira-update-agent: iniciado (installation_id=... holder=... ledger=... poll_interval=...)`. Sem nenhum run no ledger ainda, o daemon fica ocioso (reconcile não encontra runs ativos) — isto é esperado; `status` só lista algo depois do primeiro `updatectl start` (ver `02-release-e-promocao.md`).

`updatectl status` lista só runs e recuperação pendente — **nunca mostra o heartbeat** (um daemon saudável e um parado produzem a mesma saída `nenhum run registrado para esta instalação` numa instalação recém-instalada, sem nenhum run ainda). Para confirmar de fato que o daemon está vivo (tabela `agent_heartbeat`, gravada a cada 30s independente do ciclo de reconcile — ver `apps/update-agent/cmd/omnira-update-agent/main.go`), consulte o ledger diretamente (leitura pura, nunca muta nada, seguro mesmo sem o lock da instalação):

```sh
python3 -c "
import sqlite3, sys
con = sqlite3.connect('/var/lib/omnira-update-agent/ledger.db')
print(con.execute('SELECT installation_id, holder, last_poll_at, observed_at FROM agent_heartbeat').fetchall())
"
```
Uma linha com `last_poll_at` recente (atualizada a cada ~30s) confirma o daemon vivo. Nenhuma linha, ou `last_poll_at` parado, indica que o daemon não está rodando ou travou — confira `journalctl -u omnira-update-agent` antes de prosseguir. Com a Update Manager API habilitada, o mesmo sinal está em `GET /api/v1/platform/monitoring/status` (`agent_heartbeat_seen`/`agent_stale`, ver `08-troubleshooting.md`).

## Erro comum: diretório do ledger errado

Um achado real do Codex durante F9: o default antigo de `--ledger-path` (`/var/lib/omnira-agent/...`) não batia com o `StateDirectory=omnira-update-agent` que o systemd cria — o daemon falhava ao abrir o ledger porque o usuário de serviço não tem permissão para criar outro diretório solto em `/var/lib`. O default atual de `--ledger-path` já está alinhado (`/var/lib/omnira-update-agent/ledger.db`); se você sobrescrever `OMNIRA_AGENT_LEDGER_PATH` manualmente, garanta que o caminho fica dentro do `StateDirectory` real.
