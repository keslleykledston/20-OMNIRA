# Provisionamento do Service Hub (operador)

Estado: **ferramenta pronta e testada; nada implantado**. A API HTTP do Hub (`OMNIRA_HUB_API_ENABLED`) e o projetor (`OMNIRA_HUB_PROJECTOR_ENABLED`) continuam **desligados** por padrão, e as migrations 093–097 ainda **não** foram aplicadas no banco vivo.

Não existe API HTTP de administração: o OMNIRA não tem o conceito de "administrador de plataforma" para humanos (`system_admin` é só um modo interno de sessão). Até esse modelo existir, o provisionamento é feito por **`omnira-hubctl`**, executado por um operador no servidor.

## O modelo (em uma frase)
`membro do hub` **não** dá acesso a nenhum tenant. O acesso a um tenant exige as três coisas ao mesmo tempo: o usuário é **membro** do hub, existe um **contrato** vivo hub↔tenant, e existe um **grant** vivo do usuário nesse tenant. Revogar qualquer uma das três corta o acesso imediatamente (RLS).

## Como executar
Mesma imagem do API; a variável `OMNIRA_DATABASE_URL` já aponta para o papel da aplicação (`omnira_app`). A ferramenta **recusa** rodar como dono do banco (sem `OMNIRA_ALLOW_PRIVILEGED_DB=true`).

```bash
docker compose exec api /app/omnira-hubctl --operator <seu-nome> <comando> ...
```
`--operator` é obrigatório e vai para **todo** evento de auditoria (`audit_events`, `metadata.operator`).

| Comando | O que faz |
|---|---|
| `hub create --name N [--description D]` | cria o hub (imprime o id) |
| `hub status --hub ID --status active\|suspended` | suspende/reativa o hub inteiro |
| `member add --hub ID (--user ID \| --email E) [--role hub_agent\|hub_admin]` | põe o usuário no hub (**sem** acesso a tenants) |
| `member remove --hub ID (--user ID \| --email E)` | remove; os grants dele saem junto |
| `contract create --hub ID --tenant ID [--valid-until RFC3339] [--queues ID,ID]` | cria o contrato; `--queues` restringe às filas **desse tenant** |
| `contract status --hub ID --tenant ID --status active\|suspended\|revoked` | muda o contrato (corta todos os grants atrás dele) |
| `grant add --hub ID --tenant ID (--user ID \| --email E) [--valid-until RFC3339] [--reply]` | concede (ou renova) o acesso do usuário ao tenant. **Sem `--reply` o acesso é somente leitura**; com `--reply` o agente pode assumir e responder (ADR-0037). Renovar sem `--reply` remove a capacidade |
| `grant revoke --hub ID --tenant ID (--user ID \| --email E)` | revoga |
| `show --hub ID` | membros, contratos e grants do hub (somente leitura) |
| `platform-operator add\|revoke (--user ID \| --email E)` / `platform-operator list` | quem pode (futuramente) criar empresas/Hubs e ligar/desligar empresas pelo Hub (ADR-0038; migration 099). Não há rota HTTP: só este comando concede ou retira |
| `reconcile` | projeta uma vez as conversas na inbox do Hub (o worker faz isso no intervalo quando `OMNIRA_HUB_PROJECTOR_ENABLED=true`); útil logo após provisionar |

## Regras que a ferramenta impõe (testadas)
- O `contract_id` **nunca** é informado: é derivado de hub + tenant.
- O usuário precisa estar **ativo** e ser **membro** do hub; o contrato precisa estar **ativo** e dentro da validade; o hub precisa estar **ativo**.
- Filas do `--queues` precisam existir **e ser do tenant do contrato** (fila de outro tenant é recusada).
- Lista de filas vazia é recusada (daria "nenhuma fila"); omita `--queues` para cobrir todas.
- `--email` deve achar **exatamente um** usuário (zero ou vários = erro).
- Cada operação é uma transação com o evento de auditoria **dentro** dela: ou os dois acontecem, ou nenhum.
- Repetir um comando é seguro (membro igual, grant igual, revogar de novo não duplicam nada nem a auditoria).

## Receita: dar a um atendente acesso a um cliente
```bash
H=$(docker compose exec -T api /app/omnira-hubctl --operator ana hub create --name "K3G Service Desk" | awk '{print $NF}')
docker compose exec api /app/omnira-hubctl --operator ana member   add    --hub $H --email maria@k3g.example
docker compose exec api /app/omnira-hubctl --operator ana contract create --hub $H --tenant <tenant-id> --valid-until 2027-01-01T00:00:00Z
docker compose exec api /app/omnira-hubctl --operator ana grant    add    --hub $H --tenant <tenant-id> --email maria@k3g.example --reply   # sem --reply: somente leitura
docker compose exec api /app/omnira-hubctl --operator ana show --hub $H
```
Encerrar: `grant revoke` (uma pessoa), `contract status ... --status revoked` (o cliente inteiro), `hub status ... --status suspended` (o hub inteiro).

## Verificar o conjunto com os binários reais (sem tocar em nada real)
`scripts/e2e-hub-smoke.sh` sobe Postgres e NATS descartáveis, aplica as migrations, provisiona com a `omnira-hubctl`, projeta, sobe o `omnira-api` real com login de desenvolvimento e confere por HTTP (14 verificações, inclusive revogação e flag desligada). Não usa Keycloak nem canal real.

## Antes de usar em produção (pendências do dono)
1. Aplicar as migrations 093–097 (com backup antes: `scripts/backup-omnira-db.sh`, depois `docker compose up migrate`).
2. Reconstruir a imagem do API (o `omnira-hubctl` entra pelo `Dockerfile.api`).
3. Ligar `OMNIRA_HUB_PROJECTOR_ENABLED=true` no worker (preenche a inbox do Hub) e `OMNIRA_HUB_API_ENABLED=true` no API.
4. Nada disso foi feito; esta etapa só entregou e testou o código.


## Gestão de empresas pelo Hub (ADR-0038 fase 1)
Para criar e suspender empresas e ligar/desligar capacidades pela tela **Hub › Empresas**:
1. Migrations 098–100 aplicadas (backup antes) e imagem do API/worker/web reconstruída.
2. A pessoa precisa ser **operador de plataforma** e **hub_admin** do Hub:
   ```bash
   docker compose exec -T api /app/omnira-hubctl --operator ana platform-operator add --email pessoa@k3g.example
   docker compose exec -T api /app/omnira-hubctl --operator ana member add --hub $H --email pessoa@k3g.example --role hub_admin
   ```
3. `OMNIRA_HUB_API_ENABLED=true` e `OMNIRA_HUB_ADMIN_API_ENABLED=true` no `.env`; recriar o `api`.
4. Uma empresa criada pela tela nasce **sem acesso para ninguém**: conceda o acesso aos atendentes com `contract`/`grant` (a empresa já nasce com o contrato).
Desligar tudo: `OMNIRA_HUB_ADMIN_API_ENABLED=false`. As capacidades já desligadas continuam valendo no servidor (é dado, não flag).

## Painel de Acessos (ADR-0039) — pessoas e permissões por instância
Para gerenciar administradores das instâncias, agentes e o que cada um pode fazer em cada instância pela tela **Acessos** (`/acessos`):
1. Migration 101 aplicada (backup antes) e imagem do API/web reconstruída.
2. `OMNIRA_HUB_API_ENABLED=true` e `OMNIRA_HUB_ACCESS_API_ENABLED=true` no `.env`; recriar o `api`. **Não** exige ser operador de plataforma: basta ser `hub_admin` do Hub.
3. Quem é `hub_admin` continua sendo definido só pelo `hubctl` (`member add --role hub_admin`); a tela não cria nem remove administradores do Hub.
4. A pessoa a ser adicionada como agente ou administrador de instância **já precisa ter conta** no OMNIRA (e-mail exato). Pessoa nova: o administrador da empresa a convida em "Equipe"; depois o administrador do Hub a libera nas demais instâncias.
5. Regra 5: o administrador de uma empresa **não consegue** convidar quem já atua em outra instância (409); isso é do administrador do Hub, pela tela Acessos.
6. `hubctl grant add` sobre um grant que já existe só mantém ou reduz o acesso; reativar revogado, tirar/estender validade ou subir de leitura para resposta exigem `--renew`.
Desligar: `OMNIRA_HUB_ACCESS_API_ENABLED=false` (as concessões feitas continuam valendo: são dados). "Conversas" unificadas (2+ empresas) dependem só de `OMNIRA_HUB_API_ENABLED`.
