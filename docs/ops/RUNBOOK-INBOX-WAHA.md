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

## Primeiro acesso (DEV)
O login é o **mock** do backend (`POST /api/v1/auth/login {email}`): só conhece `test@omnira.local` (agente) e `admin@omnira.local` (admin do tenant `11111111-…`). O seed cria tenant, usuários, memberships e fila padrão. Qualquer senha serve. **Não há IdP real** (ver bloqueios).

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

## Bloqueios para produção (não declarar `PRODUCTION_READY`)
Veja `docs/delivery/ROADMAP-TO-GOAL.md` (pendências) — principalmente: **IdP real (OIDC)**, gate formal + piloto supervisionado, TLS/edge configurado, retenção/limpeza do Outbox, observabilidade validada.
