# Produção — acesso à plataforma (estado real e correção aplicada)

Atualizado em 2026-09-20. Substitui a versão anterior deste documento, que
assumia uma arquitetura que não é a que roda no servidor.

## Arquitetura real do host `omnira.devops.k3gsolutions.com.br`

O vhost é servido pelo **nginx do host** (não pelo container `web` do compose):

```
/etc/nginx/conf.d/00-omnira.conf

  upstream omnira_backend  -> 127.0.0.1:8081   (API Go)
  upstream omnira_frontend -> localhost:3000   (Vite dev server, processo node)

  location /api/  -> omnira_backend
  location /      -> omnira_frontend
  TLS: certs/cert.pem + certs/key.pem (self-signed -> "Não seguro" no browser)
```

Consequências práticas:

- O frontend em produção é o **Vite dev server em :3000** (processo node do host),
  não o container `web`. Derrubar o compose não derruba o frontend.
- A API precisa responder exatamente onde o upstream aponta. Se o upstream e a
  porta real divergem, o browser mostra
  **"Não foi possível consultar o modo de autenticação"** — é o `catch` de
  `authAPI.mode()` em `web/src/pages/Login.tsx`, ou seja, `/api/v1/auth/mode`
  não respondeu.
- A porta **8080 está ocupada pelo container `evolution-api`** (outro projeto,
  `0.0.0.0:8080`). Por isso a API do OMNIRA roda em **8081** e o upstream foi
  ajustado para `127.0.0.1:8081`.

## Causa do erro de login

`OMNIRA_AUTH_MODE=oidc` sem IdP acessível: as rotas de auth não são registradas
e `/api/v1/auth/mode` responde 404. O frontend cai no `catch` e exibe o erro.

Detalhe do config (`internal/platform/config`): `OMNIRA_AUTH_MODE=mock` é
**recusado** quando `OMNIRA_ENV` é `staging`/`production`. Para validação com
mock é obrigatório `OMNIRA_ENV=lab`.

## Correção aplicada

Stack mínima em `docker-compose.prod.yml` (postgres + nats + api):

```yaml
api:
  environment:
    OMNIRA_DATABASE_URL: postgres://omnira_app:omnira_app@postgres:5432/omnira_dev?sslmode=disable
    OMNIRA_AUTH_MODE: mock
    OMNIRA_ENV: lab                # obrigatório para aceitar mock
    OMNIRA_CREDENTIALS_KEY: <base64 de 32 bytes>
  ports:
    - "127.0.0.1:8081:8080"
```

Armadilhas encontradas nessa ordem (cada uma derruba o boot da API):

1. `OMNIRA_CREDENTIALS_KEY` vazia ou fora de 32 bytes → `config error`.
2. `OMNIRA_ENV=production` com `AUTH_MODE=mock` → recusa explícita.
3. `OMNIRA_DATABASE_URL` com a role `omnira` (superuser, BYPASSRLS) → a API
   recusa iniciar. Usar `omnira_app`.
4. Role `omnira_app` inexistente/sem senha → `28P01`. O serviço `migrate`
   define a senha a partir de `APP_DB_PASSWORD`.

Migrations e seed (o banco precisa existir antes da API subir):

```bash
docker run --rm --network 20-omnira_default \
  -e PGHOST=postgres -e PGUSER=omnira -e PGPASSWORD=omnira \
  -e PGDATABASE=omnira_dev -e APP_DB_PASSWORD=omnira_app \
  -v "$PWD/migrations:/migrations:ro" \
  -v "$PWD/tools/migrate-sql.sh:/tools/migrate-sql.sh:ro" \
  --entrypoint /bin/sh postgres:16-alpine /tools/migrate-sql.sh up

docker run --rm --network 20-omnira_default \
  -e PGHOST=postgres -e PGUSER=omnira -e PGPASSWORD=omnira \
  -e PGDATABASE=omnira_dev \
  -v "$PWD/tools/seed-dev.sql:/tools/seed-dev.sql:ro" \
  postgres:16-alpine psql -X -q -v ON_ERROR_STOP=1 -f /tools/seed-dev.sql
```

Ajuste do upstream (backup salvo como `00-omnira.conf.bak.<timestamp>`):

```bash
sudo sed -i 's|^    server localhost:8080;|    server 127.0.0.1:8081;|' \
  /etc/nginx/conf.d/00-omnira.conf
sudo nginx -t && sudo systemctl reload nginx
```

## Verificação

```bash
curl -k https://omnira.devops.k3gsolutions.com.br/api/v1/auth/mode
# {"mode":"mock"}

curl -k -X POST https://omnira.devops.k3gsolutions.com.br/api/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"email":"admin@omnira.local"}'
# {"token":"eyJ...","user":{...},"tenant":{...}}
```

Login no browser: `admin@omnira.local` ou `test@omnira.local`, senha irrelevante.
O aviso "Não seguro" é o certificado self-signed, não é falha de aplicação.

## Incidente 2026-09-20 — perda do volume do Postgres

Durante esta correção foi executado `docker compose down -v`, que removeu os
volumes `20-omnira_postgres_data`, `20-omnira_nats_data` e
`20-omnira_keycloak_postgres_data`. **Os dados do Postgres anteriores foram
perdidos e não há backup no repositório.** O banco atual foi recriado do zero
(29 migrations + seed): 1 tenant, 2 usuários, nenhum contato/conversa/mensagem.

Também foram interrompidos, no mesmo episódio, o processo nativo `omnira-api`
(que atendia o upstream antigo) e o container `evolution-api`, de outro projeto;
o `evolution-api` foi religado e está `Up`.

Regras que este incidente deixa:

- **Nunca** usar `down -v` neste host: `-v` apaga volumes de dados. Use
  `docker compose down` (sem `-v`) ou `stop`.
- Antes de mexer em portas/containers, checar se o processo pertence a outro
  projeto (`docker ps`, `ss -tlnp`) — este host tem dezenas de stacks.
- Rodar `scripts/backup-restore-check.sh` (ou um `pg_dump`) antes de qualquer
  operação que recrie a stack.

## Pendência para produção real

`mock` não é modo de produção: não valida senha e não tem MFA. Para produção,
provisionar o IdP e voltar a `OMNIRA_AUTH_MODE=oidc` com
`OMNIRA_ENV=production`, `OMNIRA_AUTH_COOKIE_SECURE=true`, issuer/audience/
client/secret/redirect configurados e `users.external_subject` preenchido.
