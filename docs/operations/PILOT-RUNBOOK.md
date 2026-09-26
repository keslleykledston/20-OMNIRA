# Pilot Runbook — Auth, Backup, TLS, Frontend, Deploy/Rollback

Procedimentos operacionalmente verificados (PILOT.1–PILOT.3, 2026-09-26).
Cada um foi executado de ponta a ponta neste ambiente antes de ser
documentado aqui — isto não é uma spec aspiracional (para isso, ver
`DISASTER-RECOVERY.md`).

## 1. Autenticação (Keycloak / OIDC)

### Health check

```bash
docker inspect -f 'health={{.State.Health.Status}} restarts={{.RestartCount}}' omnira-keycloak
```

Esperado: `health=healthy restarts=0`. Se `restarts` cresce continuamente, o
container está em crash-loop — ver logs (`docker logs omnira-keycloak --tail
50`) antes de qualquer outra ação.

### Smoke test de login (sem navegador)

```bash
curl -s https://auth.devops.k3gsolutions.com.br/realms/omnira/.well-known/openid-configuration \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['issuer'])"
```

Esperado: `https://auth.devops.k3gsolutions.com.br/realms/omnira`, byte-idêntico
ao valor de `OMNIRA_AUTH_ISSUER` no `.env` — se divergir, todo login real
falhará com `iss` mismatch na validação do ID token, mesmo com credenciais
corretas. A API deve alcançar esse MESMO endpoint público (não um hostname
Docker-only) com validação TLS normal:

```bash
docker exec omnira-api sh -c "wget -qO- https://auth.devops.k3gsolutions.com.br/realms/omnira/.well-known/openid-configuration" | head -c 100
```

### O que fazer se o login real falhar

1. Confirmar `docker logs omnira-keycloak` — se aparecer `Unrecognized
   field "X"`, o `deploy/keycloak/omnira-realm.json` tem um campo inválido
   para a versão do Keycloak em uso; a importação falha por inteiro, não
   parcialmente.
2. Confirmar `clientAuthenticatorType` do client `omnira-web` é
   `"client-secret"` — outros valores (`client-secret-post`,
   `client-secret-basic`) não são reconhecidos pelo fluxo de autenticação de
   client desta instalação e todo `client_secret` é rejeitado com
   `invalid_client`/`client_not_found`, mesmo com o secret certo.
3. Confirmar `OMNIRA_AUTH_CLIENT_SECRET` no `.env` bate com o secret ativo
   no Keycloak (Admin Console → Clients → omnira-web → Credentials).
4. Um 401 "invalid identity token" no callback com o token exchange OK é
   esperado apenas se o código de validação (`internal/platform/authn/oidc.go`)
   tentar resolver a identidade antes de provisioná-la — isso já foi
   corrigido; se reaparecer, é uma regressão nesse arquivo.

### Provisionamento de tenant após o primeiro login

Um usuário que loga pela primeira vez é criado automaticamente
(`ProvisionIdentity`), mas sem nenhuma `membership` — `/api/v1/auth/session`
retorna `403 no active tenant membership` até uma membership ser atribuída:

```sql
INSERT INTO memberships (tenant_id, user_id, role_id, status)
SELECT '<tenant_id>', id, '<role_id>', 'active'
FROM users WHERE email = '<email>';
```

## 2. Backup do banco `omnira_dev`

### Comando canônico

```bash
scripts/backup-omnira-db.sh
```

- Formato: `pg_dump -Fc` (mesmo formato provado restaurável por
  `scripts/backup-restore-check.sh`).
- Destino: `backups/omnira_dev/` (git-ignorado).
- Cada dump vem com um `.meta.json` irmão (timestamp, tamanho,
  `schema_version`, operador).
- Falha (exit non-zero) se o dump não rodar ou o artefato for suspeito
  (< 1KB); nunca imprime credenciais.

### Automação

Cron do usuário `suporte` neste host:

```
5 * * * * cd <repo> && ./scripts/backup-omnira-db.sh >> backups/omnira_dev/backup.log 2>&1
```

- **Frequência:** a cada hora.
- **RPO declarado:** ≤ 1 hora (tempo desde o último dump bem-sucedido).
- **Retenção:** 7 dias (poda automática no próprio script).
- **Responsável operacional:** quem administra este host (`suporte`); o
  cron roda como esse usuário via `docker exec`, sem senha de banco exposta.
- Isto é o mecanismo apropriado para escala de LAB/pilot. Não é a estratégia
  de PITR/off-host multi-AZ descrita como alvo em `DISASTER-RECOVERY.md` para
  produção geral — ver aquele documento para o que falta antes de qualquer
  promoção de estado além de `LIMITED_INTERNAL_PRODUCTION`.

**Importante — isto é estado do HOST, não do repositório.** O crontab acima
foi instalado manualmente neste host (`crontab -e` do usuário `suporte`);
clonar este repositório em outra máquina, ou reconstruir este host do zero,
**não recria o agendamento**. `scripts/backup-omnira-db.sh` é versionado;
a entrada de cron que o invoca periodicamente não é.

**Verificar que o cron existe** (rodar após qualquer rebuild/troca de host):

```bash
crontab -l | grep backup-omnira-db.sh
```

Deve retornar a linha `5 * * * * ...` acima. Se retornar vazio, o
agendamento não existe neste host e o RPO de 1h não está sendo cumprido —
reinstalar com:

```bash
( crontab -l 2>/dev/null | grep -v "backup-omnira-db.sh" ; \
  echo "5 * * * * cd $(pwd) && ./scripts/backup-omnira-db.sh >> backups/omnira_dev/backup.log 2>&1" ) | crontab -
```

### Verificar um backup

```bash
ls -la backups/omnira_dev/*.dump | tail -5
cat backups/omnira_dev/<arquivo>.meta.json
```

Um `.dump` com `size_bytes` compatível com o histórico (ver `.meta.json`
anteriores) e um `schema_version` igual à última migration em
`migrations/*.up.sql` é um backup válido. O próprio script já recusa
(exit 1) qualquer artefato menor que 1KB.

### Restore — mecanismo já provado (não destrutivo)

```bash
scripts/backup-restore-check.sh
```

Roda dump/restore contra bancos **descartáveis** (`omnira_bkp_src`,
`omnira_bkp_restored`) e um container Postgres **descartável**
(`omnira-bkp-fresh`) — nunca toca `omnira_dev`. Usar isto para validar
periodicamente que o mecanismo de restore continua funcionando, e para
medir RTO, sem qualquer risco ao banco vivo.

### Restore de incidente real (destrutivo — só sob decisão humana)

Nenhum restore contra o `omnira_dev` vivo foi executado nesta sessão — o
procedimento abaixo é o que `backup-restore-check.sh` já prova funcionar,
adaptado para o banco real, e exige aprovação explícita antes de rodar:

```bash
# 1. Parar API/worker (evita escritas durante o restore)
docker compose stop api worker

# 2. Restaurar o dump escolhido para o omnira_dev real
#    (pg_restore --clean recria objetos; --if-exists evita erro em objetos ausentes)
docker exec -i omnira-postgres pg_restore -U omnira -d omnira_dev --clean --if-exists \
  < backups/omnira_dev/<arquivo>.dump

# 3. Confirmar schema_version pós-restore bate com o .meta.json do dump usado
docker exec omnira-postgres psql -U omnira -d omnira_dev -tA -c \
  "SELECT MAX(version) FROM schema_migrations"

# 4. Rodar migrations pendentes se o restore for de um dump mais antigo que o código atual
docker compose up migrate

# 5. Subir API/worker de novo e confirmar health
docker compose up -d api worker
```

Distinção importante: isto é uma restauração de **dados** a partir de um
dump — não confundir com `migrate down` (rollback de schema), que é uma
operação diferente e não substitui um restore de desastre.

## 3. TLS / borda pública

Dois hostnames públicos, dois vhosts, dois certificados Let's Encrypt
separados (não wildcard) — ambos emitidos e geridos pelo `certbot.timer`
(systemd) já ativo neste host, sem cron adicional:

| Hostname | vhost | Upstream | Propósito |
|---|---|---|---|
| `omnira.devops.k3gsolutions.com.br` | `/etc/nginx/conf.d/00-omnira.conf` | `127.0.0.1:28080` (`/api/`), `127.0.0.1:3000` (`/`) | app + API |
| `auth.devops.k3gsolutions.com.br` | `/etc/nginx/conf.d/01-omnira-auth.conf` | `127.0.0.1:8888` | Keycloak (browser + API, issuer canônico) |

- Terminação TLS real acontece no Nginx do **host**, não no Nginx interno do
  container (`web/nginx.conf`, HTTP puro, uso exclusivo da rede Docker).
- Redirect HTTP→HTTPS confirmado nos dois vhosts; proxy headers e cabeçalhos
  de segurança presentes; timeout longo (3600s) para SSE no vhost do app.
- A porta de management do Keycloak (9000) nunca é exposta publicamente — o
  vhost `auth.*` só faz proxy para a porta browser-facing (8888).
- `OMNIRA_AUTH_COOKIE_SECURE=true` é obrigatório com esses hostnames
  públicos (HTTPS real) — nunca volte para `false` fora de teste local puro.

### Verificação rápida

```bash
curl -Ik https://omnira.devops.k3gsolutions.com.br/ | head -3   # espera 200, sem aviso de certificado
curl -Ik https://auth.devops.k3gsolutions.com.br/realms/omnira/.well-known/openid-configuration | head -3
```

Nunca aceitar `-k`/`--insecure` como prova válida de TLS — se a validação
normal falhar, o certificado está com problema, não o comando.

### Renovação

```bash
systemctl is-active certbot.timer   # espera "active"
sudo certbot certificates           # confirma os dois hostnames, dias até expirar
```

## 4. Frontend — topologia de produção

**Antiga:** processo `vite` rodando diretamente no host (`web/node_modules/.bin/vite`),
na porta 3000, fora do Docker Compose. Nunca mais usar isso para o pilot.

**Atual (PILOT.3):** o serviço `web` do `docker-compose.yml` já implementava a
topologia correta — só nunca tinha sido ativado (a porta 3000 estava ocupada
pelo Vite). Build multi-stage (`web/Dockerfile`): `npm run build` (que já
roda `tsc` — typecheck incluso) gera `web/dist/`, servido por
`nginx:alpine` (`web/nginx.conf`) com:

- SPA fallback (`try_files $uri $uri/ /index.html`) — rotas diretas
  (`/inbox`, `/contacts`, `/tickets`, `/ticket-reconciliation`,
  `/supervisor`, `/channels`, etc.) funcionam em reload direto do navegador.
- Assets com hash (`index-<hash>.js/css`) servidos com
  `Cache-Control: public, immutable, max-age=31536000`; `index.html` nunca
  cacheado indefinidamente (`expires -1`).
- Source maps: **desabilitados** em produção (não gerados pelo `vite build`
  por padrão neste projeto) — aceitável para o estágio atual do pilot.
- `/api/` proxied internamente para `api:8080` (redundante com o host Nginx,
  que já roteia `/api/` diretamente para `127.0.0.1:28080` — mantido por se
  alguém acessar o container `web` sem passar pelo host Nginx).

O host Nginx (`00-omnira.conf`) **não precisou de nenhuma mudança**: já
apontava `/` para `localhost:3000`; a troca foi só o que ocupa essa porta
(Vite → container `omnira-web` de produção).

### Ativar (se `omnira-web` estiver `Created`/parado)

```bash
# a porta 3000 do host precisa estar livre — se o Vite manual ainda estiver
# rodando, encerre-o primeiro (não mate a esmo: confirme o PID antes)
docker compose up -d web
docker inspect -f 'health={{.State.Health.Status}}' omnira-web   # espera "healthy"
```

## 5. Deploy e rollback

### Comando canônico

```bash
scripts/deploy.sh
```

Builda e recria **somente** frontend, API, worker e migrations forward —
**nunca** reinicia infraestrutura compartilhada (Postgres, NATS, Valkey,
Keycloak, e especialmente **WAHA**, cujo restart arrisca a sessão real do
WhatsApp). O script:

1. valida pré-requisitos (docker, compose v2, `.env`);
2. roda testes + typecheck + build de produção do frontend;
3. builda as imagens `api`, `worker`, `web`;
4. etiqueta cada imagem com o SHA curto do commit atual (`docker tag
   20-omnira-<svc>:latest 20-omnira-<svc>:<sha>`) — a imagem anterior
   permanece no image store local até ser explicitamente removida, servindo
   de alvo de rollback;
5. valida `docker compose config`;
6. aplica migrations **forward only** (`docker compose up migrate`);
7. recria `api`, `worker`, `web` (nesta ordem), aguardando cada um ficar
   `healthy` antes de seguir;
8. roda um smoke público (app + issuer OIDC) com validação TLS normal.

Falha em qualquer etapa aborta com exit não-zero antes de seguir adiante.

### Identificação de versão

```bash
docker images | grep -E "20-omnira-(api|worker|web)"
```

Cada imagem relevante fica etiquetada com o SHA curto do commit que a gerou
(além de `:latest`). Nenhum registry externo é necessário para o pilot — as
tags vivem no image store local do Docker deste host.

### Rollback de aplicação — nunca `migrate down` como estratégia

**Sempre confirme que a imagem-alvo do rollback existe antes de qualquer
retag.** Se não existir, o artefato "bom anterior" não está disponível
localmente — pare e não tente reconstruir aquele SHA a partir da working
tree atual (que pode já ter mudado): isso geraria uma imagem diferente sob
o mesmo rótulo, exatamente o problema que a etiquetagem por SHA existe para
evitar.

```bash
docker image inspect 20-omnira-web:<sha-anterior-bom> >/dev/null \
  || { echo "rollback target image not found locally — aborting"; exit 1; }
docker tag 20-omnira-web:<sha-anterior-bom> 20-omnira-web:latest
docker compose up -d --force-recreate web
```

**API:** mesmo padrão (checar `docker image inspect` antes) com
`20-omnira-api`.

**Worker:** mesmo padrão, com `20-omnira-worker`.

**Banco de dados:** a política é **forward corrective migration**, nunca
`migrate down` como mecanismo geral de rollback (PILOT.2 provou que 16 das
52 migrations down fazem `DROP TABLE` e 16 fazem `DROP COLUMN` — destrutivas
de dados reais). Se uma release exigir mudança de schema incompatível com o
build anterior, o rollback de aplicação por si só não é suficiente — pare e
trate como incidente: escreva uma migration corretiva forward, não reverta
a anterior.

### Prova de rollback (PILOT.3, executada neste ambiente)

Falha simulada: container `web` parado deliberadamente → app público
retornou `502` → container reiniciado → app público voltou a `200` em ~5s.
Confirma o mecanismo (detectar → restaurar → recuperar); não substitui um
teste de uma imagem `N` realmente quebrada quando uma existir.

### Health gate de um deploy

Um deploy só é considerado bem-sucedido quando:
- `omnira-api`, `omnira-worker`, `omnira-web` todos `healthy`;
- app público (`https://omnira.devops.k3gsolutions.com.br/`) responde 200
  com TLS válido;
- issuer OIDC público responde 200 com TLS válido;
- banco permanece saudável (nenhuma migration falhou);
- **WAHA continua `WORKING`** se não fizer parte da release — nunca é
  reiniciado como parte de um deploy de aplicação.

## 6. Drift de rede Docker (WAHA)

Achado do PILOT.2B/PILOT.3: o `docker-compose.yml` já declara
`waha: networks: [omnira-network]` corretamente, mas o container real
(criado antes desta sessão, rodando há vários dias) estava preso apenas na
rede `default` — drift de execução, não defeito do compose. Um
`docker compose up -d --force-recreate waha` normal restauraria
automaticamente a topologia declarada (**não fazer isso na sessão real
"WORKING" sem necessidade** — reiniciar o container WAHA arrisca exigir
reautenticação/QR da conta WhatsApp conectada). Se precisar reaplicar sem
recriar o container:

```bash
docker network connect --alias waha 20-omnira_omnira-network 20-omnira-waha-1
```

## 7. Verificar WAHA após qualquer deploy

```bash
# nunca reiniciar o container só para checar — apenas consultar o estado
python3 -c "
import subprocess, requests
key = subprocess.run(['docker','exec','20-omnira-waha-1','printenv','WAHA_API_KEY'], capture_output=True, text=True).stdout.strip()
r = requests.get('http://192.168.112.3:3000/api/sessions/omnira_85af82d7-6f01-40cf-8d15-0e04df66736a', headers={'X-Api-Key': key})
print(r.json().get('status'))
"
```

Esperado: `WORKING`, inalterado por qualquer deploy de frontend/API/worker.

## 8. Visibilidade operacional da sessão WAHA (PILOT.4C)

Três conceitos permanecem **separados** — nunca junte WAHA ao liveness do
Docker: um WAHA fora do ar nunca deve tornar a API "unhealthy" nem disparar
restart loop. Degradação do provedor é visível ao operador, não acoplada a
restart.

- **Process liveness** — o processo OMNIRA está vivo? (`vibe health`,
  healthcheck do Docker de `omnira-api`/`omnira-worker`.)
- **Application readiness** — a API consegue servir suas dependências
  internas (Postgres, NATS)?
- **External provider status** — o WAHA/sessão está utilizável? É isto que
  esta seção cobre.

### Endpoint operador (já existente, reaproveitado)

`GET /api/v1/tenants/{tenant_id}/channels/waha/connections/{connection_id}`
(mesma rota do painel de conexões — `channel.manage`, hoje só `tenant_admin`;
nenhuma permissão nova foi criada nesta fase). Resposta relevante:

```json
{
  "status": "active",
  "session_status": "working",
  "checked_at": "2026-09-26T20:00:00Z"
}
```

- `session_status` vem de uma leitura **read-only** ao WAHA (`GET
  /api/sessions/{name}` — nunca reinicia, nunca gera QR).
- `checked_at` (PILOT.4C) marca quando essa leitura realmente aconteceu —
  ausente em respostas que não chamaram o provedor (list/create).
- Nunca retorna API key, credencial, QR ou payload bruto do provedor.
- **Modelo de resposta**: quando o provedor está inacessível (timeout,
  connection reset, 5xx) ou rejeita a chamada (auth), a requisição retorna um
  **erro HTTP real** (502/503) — nunca um 200 disfarçado. Quando o provedor
  responde mas a sessão não está `WORKING` (ex.: `stopped`, `failed`), a
  resposta é **HTTP 200** com `session_status` refletindo o valor real — a
  degradação faz parte do estado, não é uma falha de requisição.

### Checagem periódica (somente leitura)

`scripts/waha-session-check.sh` — chama o mesmo endpoint read-only do WAHA
(`GET /api/sessions/{name}`) diretamente, sem depender da API OMNIRA. Nunca
reinicia, reconecta ou gera QR; sai com código 0 apenas quando a sessão
esperada está `WORKING`.

```bash
WAHA_SESSION=omnira_<connection-id> \
  STATE_FILE=/var/log/omnira/waha-session-check.log \
  scripts/waha-session-check.sh
```

Cron sugerido (a cada 5 min, fora do repositório — `crontab -e` do host):

```
*/5 * * * * WAHA_SESSION=omnira_<connection-id> STATE_FILE=/var/log/omnira/waha-session-check.log /caminho/para/scripts/waha-session-check.sh >/dev/null 2>&1
```

**ACTIVE NOTIFICATION: NÃO.** Não existe hoje integração com PagerDuty/Slack/
e-mail neste projeto — este cron produz apenas um **log de estado
local e timestampado** (`STATE_FILE`, fora do Git). Isso é visibilidade, não
alerta ativo. Enquanto isso não mudar, o operador do piloto supervisionado
precisa checar esse arquivo periodicamente (ou o endpoint acima) por conta
própria; não presuma que uma falha "vai avisar alguém".

### O que fazer quando a sessão não está `WORKING`

**Nunca comece por "reiniciar o WAHA".** Ordem correta:

1. Consultar o status (endpoint acima ou o script) — confirmar se é
   `session_status` degradado (provedor respondeu) ou erro de requisição
   (provedor inacessível) — são causas diferentes.
2. Inspecionar os logs de entrega recentes por `message_id` (PILOT.4B —
   `channel delivery: ...`) para entender se mensagens já estavam falhando
   antes da degradação aparecer.
3. Verificar alcançabilidade do provedor (o container está rodando? a rede
   Docker está correta — ver seção 6?).
4. Só então avaliar se uma reautenticação/QR é **realmente** necessária.
5. Qualquer remediação do provedor/sessão (restart, novo QR) exige aprovação
   humana explícita — nunca automática.

## 9. Estabilidade do ID de mensagem WAHA (PILOT.4A0/4A1)

O worker reserva um id de mensagem estável (`reserved_provider_message_id`)
ANTES de qualquer chamada `sendText`, e reusa esse mesmo id em toda
redelivery/retry — isso fecha a janela de envio duplicado numa
crash/timeout entre o `sendText` bem-sucedido e o commit de `MarkSent`. A
garantia depende de uma propriedade RUNTIME-PROVADA (não documentada pelo
vendor) da versão exata do WAHA/GOWS hoje pinada em produção:
**gows-2026.8.2** — enviar duas vezes o mesmo id de mensagem resulta em
exatamente UMA entrega visível no WhatsApp (PILOT.4A0).

**Isso não é garantido para nenhuma outra versão.** Antes de atualizar a
imagem/engine do WAHA:

1. repetir o smoke test de idempotência do PILOT.4A0 (dois `POST
   /api/sendText` com o mesmo `id`, contato de teste dedicado, confirmar
   exatamente uma entrega visível — dois HTTP 200 sozinhos NÃO bastam como
   evidência);
2. só depois atualizar a imagem, sob pena de reabrir a janela de envio
   duplicado sem aviso.

Não é uma garantia de exactly-once global — apenas fecha a janela
específica de crash entre `sendText` e `MarkSent`, e a de timeout com
retry, para esta combinação de provedor/versão.

## 10. Se qualquer P0 falhar

Não declare o pilot pronto. Volte para a seção correspondente acima,
reproduza o smoke test e corrija a causa raiz antes de tentar novamente —
não há atalho de mock/dev auth, restore destrutivo não testado, TLS
degradado, `migrate down` como rollback, ou restart de WAHA que seja
aceitável para um piloto com tenant real e sessão WhatsApp real.
