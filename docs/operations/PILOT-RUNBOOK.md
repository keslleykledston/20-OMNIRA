# Pilot Runbook — Auth, Backup, TLS

Procedimentos operacionalmente verificados (PILOT.1, 2026-09-26). Cada um foi
executado de ponta a ponta neste ambiente antes de ser documentado aqui —
isto não é uma spec aspiracional (para isso, ver `DISASTER-RECOVERY.md`).

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
curl -s http://<KC_HOSTNAME>/realms/omnira/.well-known/openid-configuration \
  | python3 -c "import json,sys; print(json.load(sys.stdin)['issuer'])"
```

O `issuer` retornado deve ser byte-idêntico ao valor de `OMNIRA_AUTH_ISSUER`
no `.env` — se divergir, todo login real falhará com `iss` mismatch na
validação do ID token, mesmo com credenciais corretas.

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
  de PITR/off-host multi-AZ descrita como alvo em `DISASTER-RECOVERY.md`

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
  para produção geral — ver aquele documento para o que falta antes de
  qualquer promoção de estado além de `LIMITED_INTERNAL_PRODUCTION`.

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

- Terminação TLS real acontece no Nginx do **host** (`/etc/nginx/conf.d/00-omnira.conf`),
  não no Nginx interno do container (`web/nginx.conf`, HTTP puro, uso
  exclusivo da rede Docker).
- Redirect HTTP→HTTPS confirmado funcionando; proxy headers e cabeçalhos de
  segurança presentes; timeout longo (3600s) já configurado para SSE.
- **Gap conhecido:** o certificado servido é self-signed
  (`certs/cert.pem`/`key.pem`, válido até 2027-09-19) — navegadores/clientes
  não confiam nele por padrão. Ver Human Gate do PILOT.1 para a
  classificação completa e a decisão pendente sobre emitir um certificado
  confiável (`certbot`) nesta infraestrutura Nginx compartilhada.

### Verificação rápida

```bash
curl -Ik http://omnira.devops.k3gsolutions.com.br/ | head -3   # espera 301/302 -> https
curl -Ik https://omnira.devops.k3gsolutions.com.br/ | head -3  # espera 200/301, TLS handshake OK (com -k por causa do self-signed)
```

## 4. Se qualquer P0 falhar

Não declare o pilot pronto. Volte para a seção correspondente acima,
reproduza o smoke test e corrija a causa raiz antes de tentar novamente —
não há atalho de mock/dev auth, restore destrutivo não testado, ou TLS
degradado para HTTP puro que seja aceitável para um piloto com tenant real.
