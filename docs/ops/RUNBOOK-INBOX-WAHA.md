# Runbook — Inbox WhatsApp (WAHA) no OMNIRA

Estado: **LAB / INTERNAL_PILOT candidato**. Não declarar produção: veja "Bloqueios para produção" no fim.

## Arquitetura (o que roda)
```
Browser ──► web (nginx: SPA + /api, SSE sem buffer, /internal bloqueado) ──► api (Go, role omnira_app, RLS)
                                                                  │  ▲ webhook (HMAC-SHA512)
                                              Postgres 16 ◄───────┤  │
   (NOTIFY realtime, Outbox, RLS)                   ▲             ▼  │
                                                    │          waha (WhatsApp Web, gateway)
                                     worker (Go) ───┴──► NATS JetStream (jobs)  ; ponte LISTEN→NATS (realtime)
migrate (one-shot, owner)   seed (dev)
```
- **api**: REST + SSE + webhook WAHA. **worker**: publica o Outbox, consome jobs de routing e de **envio WhatsApp**, e faz a ponte realtime (Postgres `NOTIFY` → NATS → SSE).
- **Segurança**: API e worker **recusam iniciar** conectados como superuser/BYPASSRLS. Tenant sempre vem do JWT+membership (nunca do corpo/URL sozinha). Segredos de canal cifrados (AES-256-GCM, `OMNIRA_CREDENTIALS_KEY`).

## Subir do zero (compose)
```bash
cp .env.example .env   # preencha: OMNIRA_CREDENTIALS_KEY (base64 de 32 bytes), senhas, WAHA keys
docker compose --profile whatsapp-unofficial --profile dev up -d --build
```
Ordem automática: `postgres`/`nats` → **`migrate`** (owner, idempotente, registra em `schema_migrations`, aplica `OMNIRA_APP_DB_PASSWORD` em `omnira_app`) → `api`/`worker` → `web`. `--profile dev` roda o **seed de desenvolvimento** (usuários do login mock). `waha` está no profile `whatsapp-unofficial`.

Variáveis obrigatórias: `OMNIRA_CREDENTIALS_KEY`, `OMNIRA_APP_DB_PASSWORD`, `POSTGRES_PASSWORD`, `OMNIRA_WAHA_ENABLED=true`, `OMNIRA_WAHA_API_KEY` **=** `WAHA_API_KEY` (mesmo valor em texto), `OMNIRA_PUBLIC_BASE_URL` (URL que o WAHA usa para chamar a API; no compose: `http://api:8080`). Lista completa em `.env.example`.

Gerar chave: `head -c 32 /dev/urandom | base64`.

Portas: `web` 3000→80 (o nginx do host, `omnira-nginx.conf`, aponta o frontend para `localhost:3000` e a API para `localhost:8080`), `api` 8080. O serviço `nginx` do compose é um edge opcional (`--profile edge`), **fora do padrão**.

Adotar um banco já migrado à mão: `OMNIRA_MIGRATE_BASELINE=<último prefixo aplicado>` na primeira execução do `migrate`.

## Autenticação

- DEV/teste: `OMNIRA_AUTH_MODE=mock` **e** `OMNIRA_DEV_AUTH_ENABLED=true` (ambos, e só em `local`/`dev`/`development`/`lab`/`test`). `POST /api/v1/auth/dev/login {email}` conhece `test@omnira.local` e `admin@omnira.local` — não há senha, porque não existe autenticação local neste produto. Sem a flag a rota não é registrada e responde 404; com a flag ligada em staging/production a API recusa o boot. O seed cria Tenant, Usuários e memberships. O navegador recebe cookie HttpOnly; o JWT não fica no `localStorage`.
- Ambiente real: `OMNIRA_AUTH_MODE=oidc`, issuer/audience/client/secret/redirect configurados e `OMNIRA_AUTH_COOKIE_SECURE=true`. A identidade canônica é o par `(issuer, subject)` resolvido por `user_identities` — `users.external_subject` é campo legado e não serve mais como chave de login. O primeiro acesso provisiona o Usuário (JIT); a membership precisa existir para haver TenantContext.
- O callback não cria acesso a Tenant. Membership/grant persistido continua sendo a única autoridade.

## Operar o WhatsApp (admin)
1. Entrar como `admin@omnira.local` → **Canais** → marcar o **aceite de risco** → *Create connection* (gera a chave HMAC do webhook, cifrada; nunca exibida).
2. *Start session* → aparece o **QR** (renova sozinho) → no celular: WhatsApp → Aparelhos conectados → Conectar → escanear.
3. Status vira **active** e mostra o número. Use **número descartável**: automação não oficial pode causar banimento.
4. Agentes veem conversas em **Inbox**, **Assign to Me**, respondem; status `queued → sent → delivered → read`.
`Stop` encerra a sessão. Falhas de envio ficam `failed` com motivo (`rejected`, `authentication`, `session_disconnected`, `provider_unavailable`, `retries_exhausted:*`, `channel_not_active`).

## Verificar
| O quê | Comando |
|---|---|
| Go (sem Go no host) | `docker run --rm --network host -v "$PWD":/src -w /src -e GOFLAGS=-buildvcs=false -e OMNIRA_DATABASE_URL=<owner> -e OMNIRA_APP_DATABASE_URL=postgres://omnira_app:<pw>@127.0.0.1:55434/<db>?sslmode=disable -e OMNIRA_NATS_URL=nats://127.0.0.1:4222 golang:1.25 go test -count=1 -p 1 ./...` |
| Web unit | `cd web && npx tsc --noEmit && npx vitest run` |
| E2E navegador (stack real, containers avulsos) | `scripts/e2e-inbox.sh` |
| **Clean-room do compose** (stack isolada `omnira-cr`, senhas aleatórias, WAHA real, e2e pelo nginx) | `scripts/cleanroom-compose.sh` |
| WhatsApp real (precisa de telefone) | `scripts/w3-smoke.sh` |
| Migrations | `docker compose run --rm migrate` (idempotente) · status: `docker compose run --rm --entrypoint /bin/sh migrate /tools/migrate-sql.sh status` |

## Solução de problemas
- **API/worker: `refusing to start: … bypasses RLS`** → `OMNIRA_DATABASE_URL` aponta para o owner (`omnira`, superuser). Use `omnira_app`.
- **Webhook do WAHA não chega / mensagens não aparecem** → confira `OMNIRA_PUBLIC_BASE_URL` (o **WAHA** precisa alcançá-la) e reinicie a sessão (o webhook é registrado ao criar a sessão). Assinatura inválida = 401; falha ao ler a chave = 503 (WAHA reenvia).
- **`Start session` → 503** → `OMNIRA_PUBLIC_BASE_URL` ausente ou WAHA não configurado. **502** → WAHA fora do ar/chave errada.
- **QR 409** → a sessão ainda inicia (1–2 s); a UI refaz sozinha.
- **Sem realtime** → o `worker` precisa estar no ar (ponte `LISTEN`); a UI reconecta e refaz o fetch. `docker compose logs worker | grep realtime`.
- **Token inválido após reiniciar a API** → as chaves do login mock são geradas a cada boot; faça login de novo.
- **Migration falhou** → é atômica (nada parcial); corrija e reexecute o `migrate`.
- **Mensagem `queued` para sempre** → worker parado, `OMNIRA_WAHA_ENABLED` falso no worker, ou NATS fora.

## Retenção do OMNIRA_JOBS (JetStream) — PILOT.4D3-C1

**Fonte única da política**: `internal/worker/jobsstream.Config()`. Nenhum outro
código pode montar um `jetstream.StreamConfig` para `OMNIRA_JOBS` — os
consumers de routing e delivery só criam/atualizam seu **próprio** consumer
durável, nunca a política do stream.

Política canônica (código, ainda **não implantada** — ver "Ativação" abaixo):
`Retention=limits`, `MaxAge=7d`, `MaxBytes=8GiB`, `Discard=new`,
`MaxMsgs=-1` (ilimitado), `Duplicates=2min`, `Storage=file`,
`Subjects=["job.>"]`.

**Ownership no startup** (`apps/worker/cmd/omnira-worker/main.go`): logo
após abrir o contexto JetStream, o worker chama `jobsstream.Ensure(ctx, js)`
**antes** de iniciar qualquer consumer, o publisher ou o reconciliador.
`Ensure` aplica a política, lê de volta e verifica campo a campo — falha
fecha o processo (`log.Fatalf`), nada mais sobe.

**Reconciliação** (`internal/worker/delivery`): o reconciliador de envios
presos usa exatamente `jobsstream.MaxAge`/`jobsstream.ReconciliationGrace` —
nunca um valor solto. Com `MaxAge<=0` ele fica inerte (`ShouldRun()==false`);
assim que `MaxAge` for positivo (como já está no código desta fase) e o
binário for **implantado**, ele passa a rodar de verdade.

**Monitoramento**: `scripts/nats-jetstream-check.sh` — leitura via
`/jsz?streams=1&consumers=1&config=1`, nunca muta o NATS. Verifica:
existência do stream e de **ambos** os consumers (`worker-channel-send`,
`worker-routing`), drift de config (`retention`/`max_age`/`max_bytes`/
`discard` contra os `EXPECTED_*` do próprio script — uma segunda asserção
independente da política em `jobsstream`, deliberadamente não gerada a
partir do código Go), e utilização de bytes (`stream_bytes/max_bytes`):
`<70%` OK, `70–90%` WARN (exit 0), `>=90%` CRITICAL (exit 3). Sem
auto-remediação em nenhum patamar.

**Notificação externa (PILOT.4E1)**: `scripts/run-check-with-alert.sh` +
`scripts/lib/notify.sh` — wrapper stateful/deduplicado (canal
`ntfy`-compatible) que envolve este check (e o do WAHA) sem alterar sua
semântica de saúde. `CRITICAL`/`FAIL` alertam; `WARN` fica local apenas.
Implementado e provado via `scripts/test-notify-wrapper.sh` (catcher HTTP
disposable, nenhum segredo real, nenhum envio externo real); ativação ao
vivo (tópico real + edição de cron) é PILOT.4E2, gate separado, ainda
pendente. Ver `docs/operations/PILOT-RUNBOOK.md` seção 8/9 para o design
completo.

**Ativação ao vivo é PILOT.4D3-C2, não esta fase.** Este slice só muda o
binário; implantar o novo worker (que já chama `Ensure` incondicionalmente
no boot) É o ato de ativação. Antes de fazer isso em produção, rodar o
preflight: contar mensagens `queued` cujo último evento
`job.channel.send_text.v1` publicado tem `published_at` mais antigo que
`7d+60s` **e** sem intenção não publicada pendente — hoje esperado **zero**;
se não for zero na hora do C2, **parar antes de ativar**.

**Ativação parcial (correção de segurança)**: `CreateOrUpdateStream`/
`UpdateStream` pode ter sucesso mesmo que o worker falhe depois, antes de
subir consumers/reconciliador — a atualização da política do stream e o
startup do worker **não são atômicos**. Se a política for aplicada mas o
worker novo não ficar saudável: **não** suba um binário pré-C1/pré-B2 às
cegas. Ou conserte/reinicie o **mesmo** binário novo até o reconciliador
ficar disponível, ou reverta explicitamente a política do stream antes de
voltar a um binário antigo.

**Rollback seguro (correção de segurança)**: `MaxAge → 0` sozinho **desliga
o reconciliador** (B2) — nunca faça isso e já suba um binário antigo com
linhas `queued` potencialmente presas. Ordem obrigatória:
  1. Com o worker/reconciliador **novo** ainda disponível, inspecionar
     candidatos presos usando o `MaxAge` **atualmente ativo**.
  2. Garantir que os candidatos foram reconciliados (contagem chega a 0).
  3. Só então reverter a política do stream para ilimitado, se for o caso.
  4. Só depois disso é seguro restaurar um binário anterior ao B2.
Reverter a política sozinha **não recria** mensagens já expiradas no
JetStream — reconciliar antes é o único jeito de não perder trabalho durável.

## Bloqueios para produção (não declarar `PRODUCTION_READY`)
Veja `docs/delivery/ROADMAP-TO-GOAL.md` (pendências) — principalmente: configuração/aceite do **IdP real**, gate formal + piloto supervisionado, TLS/edge configurado, retenção/limpeza do Outbox e observabilidade validada.
