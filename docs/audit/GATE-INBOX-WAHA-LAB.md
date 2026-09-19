# Gate — Inbox WhatsApp não oficial (WAHA) — estado `LAB`

**Data:** 2026-09-19 · **Escopo:** vertical *login → conectar número (QR/WAHA) → receber → assumir → responder → status*, multi-tenant, via `docker compose`.
**Estado declarado: `LAB`.** Todos os gates **automatizados** abaixo estão PASS. **Não** é `INTERNAL_PILOT` nem candidato a produção: faltam aceite humano, o smoke com telefone real e autenticação real (ver "Bloqueios"). Nenhum estado proibido (`PRODUCTION_READY`, etc.) é reivindicado.

## 1. Evidência (todos reexecutados no fim da fase P6)
| Gate | Resultado | Como reproduzir |
|---|---|---|
| Build/vet/test Go (Postgres real como `omnira_app`, NATS real) | **PASS** — 340 testes passam, 0 falham, 1 skip (teste opt-in com WAHA real), 49 pacotes | `docker run … golang:1.25 go test -count=1 -p 1 ./...` (ver runbook) |
| Web | **PASS** — `tsc` limpo; vitest 44/44 | `cd web && npx tsc --noEmit && npx vitest run` |
| **Clean-room do compose** (stack isolada, senhas aleatórias, WAHA real, e2e no navegador pelo nginx) | **PASS** — 27 migrations pelo serviço `migrate`, api/worker/web healthy, headers de segurança em HTML e assets, `/internal` 404, **12/12 e2e** (login real, inbox, assumir/responder/soltar, realtime SSE sem reload, QR real, isolamento entre tenants, token inválido, **sem violações de CSP**) | `scripts/cleanroom-compose.sh` |
| Vertical sem telefone (webhook assinado → Inbox → reply → worker → acks) | **PASS** (3× com `-race`) | `go test ./internal/e2e/` |
| Migrations | **PASS** — up-all → down-all → up-all; cluster novo com banco de outro nome; runner idempotente e atômico | `tools/migrate-sql.sh` |
| **Backup/restore** | **PASS** — restore em novo DB (RTO 1,1 s) e em **cluster novo** com roles (RTO 1,3 s); 16 tabelas críticas idênticas por checksum; RLS/FORCE/policies/funções/triggers idênticos; **isolamento entre tenants comprovado depois do restore** como `omnira_app`; mutação (RLS desligado) é detectada | `scripts/backup-restore-check.sh` |
| Health | **PASS** — `/healthz` (DB+NATS), `/internal/health/live|ready`; readiness agora sonda dependências a cada chamada (503 com DB fora; teste de regressão) | `internal/platform/httpserver/health_test.go` |
| Contratos | **PASS** — OpenAPI válido (Redocly), AsyncAPI, teste de drift rotas×contrato nas duas direções | `tools/validate-specs.sh` |
| Tenancy/RLS | **PASS** — RLS completeness, isolamento A/B, `omnira_app` sem BYPASSRLS, trava de boot contra role privilegiada, 404 sem oráculo, RLS re-provado após restore | suíte Go + `TestRLSCompleteness` |
| Git | `git diff --check` limpo; sem push/tag | — |

## 2. Revisão Codex (regra K3G: CRITICAL/HIGH bloqueia)
- **Tentativa 1:** `CODEX_REVIEW_INCOMPLETE` — o sandbox de leitura do Codex falhou neste host (`bwrap … Operation not permitted`); ele não leu nada. **Não** foi tratada como "zero achados".
- **Tentativa 2:** código enviado inline (≈100 KB: arquivos de maior risco — routing/messages/channels/inbox SSE/worker/migrations 020–027/webhook/frontend de sessão/nginx/compose/migrate). Saída real: **CRITICAL 0 · HIGH 2 · MEDIUM 7 · LOW 1**. Cobertura limitada aos arquivos enviados; **não** prova ausência de achados no restante do repositório.

| # | Sev. | Achado (resumo) | Disposição |
|---|---|---|---|
| 1 | HIGH | `000022` remove a única policy de `channel_credentials` com FORCE RLS → tudo negado | **Refutado com evidência:** o Codex não viu a `000010`, que cria as policies `read/write/update/delete_tenant` (permanecem); criação de conexão, leitura da chave HMAC e webhook funcionam sob FORCE RLS (`TestPostgresChannelIsolationAndCredentials`, vertical, e2e). |
| 2 | HIGH | SSE sem limite de conexões/vida; stream de conversa inexistente | **Corrigido:** teto por usuário (10) e global (2000) → 429 + `Retry-After`, vida máxima 30 min (cliente reconecta e refaz o fetch), conversa precisa existir/ser visível (404). 3 testes novos. |
| 3 | MED | TOCTOU entre checagens de envio e inserção (ex-assignee ainda envia) | **Corrigido:** a inserção reavalia assignee/canal no **mesmo SQL** com `FOR SHARE` na conversa (a atribuição usa `FOR UPDATE`); manager isento; 409 `conversation changed`. Teste com view obsoleta. |
| 4 | MED | Chamada ao provedor com lock de linha + transação abertos | **Mitigado:** `MaxAckPending=8` (entregas em voo limitadas) e pool do worker ≥ 24 conexões. Redesenho por lease (claim curto → chamada fora da tx → finalização) fica como dívida **D-1**. |
| 5 | MED | Crash entre envio e commit reenvia (duplicata ao cliente) | **Aceito/documentado:** o WAHA não tem idempotency-key; mitigação futura = reconciliação por lookup de mensagem (**D-1**). Duplicata só em crash na janela do envio. |
| 6 | MED | Webhook anônimo sem rate limit | **Mitigado:** API publicada só em `127.0.0.1` por padrão; nginx público não encaminha `/webhooks` nem `/internal` (404). Rate limit dedicado fica em **D-2**. |
| 7 | MED | JWT em `localStorage` sem CSP | **Mitigado:** CSP estrita + headers de segurança em toda location (o `add_header` por location anulava os do server — corrigido com snippet), e2e sem violações. Migrar para cookie HttpOnly junto do IdP real (**D-3**). |
| 8 | MED | nginx do web só HTTP, sem HSTS | **Por desenho:** o container `web` é HTTP atrás do TLS do host (`omnira-nginx.conf`, agora com HSTS). Sem terminador TLS o deploy é inseguro — documentado no runbook. |
| 9 | MED | `realtime_emit` executável por PUBLIC → forjar NOTIFY de outro tenant | **Aceito:** o evento carrega só ids e a UI refaz o fetch pela API autorizada (nenhum dado vaza; no pior caso, refetch espúrio). Qualquer role pode chamar `pg_notify` diretamente, então revogar a função não fecharia o vetor; validação de proveniência fica em **D-4**. |
| 10 | LOW | FK de credencial só por `connection_id` | **Diferido (D-5):** FK composta `(tenant_id, id)`. Inserção direta exige privilégio de escrita já restrito a admin por RLS. |

## 3. Registro de dívidas (para o próximo agente)
D-1 entrega por lease + reconciliação (duplicata/lock) · D-2 rate limit no webhook e no login · D-3 cookie HttpOnly/SameSite + IdP OIDC · D-4 proveniência do NOTIFY · D-5 FK composta · retenção/limpeza do Outbox (`outbox_events` cresce) · otel-collector sem healthcheck/destino validado e métricas de negócio só via OTel (o `/metrics` expõe apenas gauges de saúde) · erros ainda `text/plain` (sem Problem Details) · mídia (inbound/outbound) e templates · telas legadas com dados mock · CI que rode `cleanroom-compose.sh`/`backup-restore-check.sh`. Detalhes por fase em `docs/delivery/ROADMAP-TO-GOAL.md`.

## 4. Bloqueios para o próximo estado
1. **Smoke com telefone real** (`scripts/w3-smoke.sh`): pareamento, recebimento e ack reais **não** foram exercitados (o vertical usa WAHA fake; o QR é real).
2. **Autenticação real (OIDC)**: só existe login *mock* (e-mails fixos; chaves RSA geradas a cada boot).
3. **Aceite humano** deste gate e piloto supervisionado.
4. TLS/edge de produção configurado e testado; políticas de retenção de dados; observabilidade validada.

## 5. Aceite
| Papel | Nome | Data | Decisão |
|---|---|---|---|
| Responsável técnico | | | |
| Aceite de produto | | | |
