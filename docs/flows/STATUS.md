# Flow Builder — status e evidências

Branch `feat/flow-builder` (base `main` @ `8af6d54`). Estado de produção: **LAB** (nada implantado, flag desligada). Este arquivo é atualizado a cada fase; só registra o que foi executado.

## Como verificar (Docker; o host não tem Go)
```bash
docker run --rm --network host -v "$PWD":/src -v omnira-gomod:/go/pkg/mod -w /src \
  -e GOFLAGS=-buildvcs=false -e OMNIRA_INTEGRATION_TEST=1 \
  -e OMNIRA_DATABASE_URL='postgres://omnira:omnira@127.0.0.1:55434/omnira_test_flows?sslmode=disable' \
  -e OMNIRA_APP_DATABASE_URL='postgres://omnira_app:omnira_app@127.0.0.1:55434/omnira_test_flows?sslmode=disable' \
  golang:1.25 go test -count=1 -p 1 -timeout 150s ./internal/flows/...
```
Banco **descartável** `omnira_test_flows` (criado com todas as `migrations/*.up.sql`); nunca `omnira_dev`; **sem** URL de NATS (o broker vivo não é usado em teste).

## Baseline (antes da feature)
`main` @ `8af6d54` + 2 consertos de build isolados (commit `fix(build)`): **64 pacotes ok, 5 falhando — todos preexistentes**.

| Pacote | Causa | Classe |
|---|---|---|
| `internal/platform/authn` (testes) | `http_handler_test.go:21` — assinatura de `NewAuthHandler` mudou (IAM5), teste não acompanhou | PREEXISTENTE / TEST |
| `internal/platform/httpserver` (testes) | `contract_test.go:82`, `dev_auth_route_test.go` — `RegisterAuthHandlers` ganhou argumentos | PREEXISTENTE / TEST |
| `internal/tenancy/adapters` (testes) | `invitations_password_test.go:7` — import `net/http` sem uso | PREEXISTENTE / TEST |
| `internal/intelligence/adapters` | `TestQueriesStayFastOnABusyConversation`: 739 ms > limite 400 ms (sensível a carga do host) | PREEXISTENTE / ENV |
| `internal/outbox/adapters` | `TestStoreUsesInjectedTransaction`: "runtime system session could not read committed outbox event" | PREEXISTENTE / a investigar |

Quebras de **código de produção** em `main` (impediam `go build ./...`), corrigidas em commit separado e identificado, sem relação com Flow Builder:
1. `internal/platform/authn/password_handler.go`: import `platformdb` sem uso.
2. `internal/platform/httpserver/server.go:214`: `HandleFunc` recebendo `http.Handler` (trocado por `Handle`).
Se a frente IAM5 já corrigiu, o commit pode ser descartado sem afetar a feature.

## Fases
| Fase | Escopo | Estado |
|---|---|---|
| 0 | Backup, análise de conflito, ADR-0019, baseline | **feita** |
| 1 | Migrations 082–084, domínio, repositório Postgres + testes de isolamento | **feita** (ver evidências) |
| 2 | Control plane: validador, publicar/rollback, RBAC, HTTP, OpenAPI | pendente |
| 3 | Runtime: resolver, passo transacional, wait/resume, idempotência, limites, gancho de ingest, worker | pendente |
| 4 | Nodes determinísticos + SystemSender | pendente |
| 5 | Simulador | pendente |
| 6 | Templates/Packs (embed), instalador, packs Starter/K3G/ISP NOC + testes | pendente |
| 7 | Frontend em `web/` (não em `apps/web`) | pendente |
| 8 | Nodes de IA, analytics/observabilidade, production gate, relatório final | pendente |

## Evidências por fase
**FLOW.1** (banco descartável, 84 migrations aplicadas do zero):
- `go test ./internal/flows/...`: domínio (3) + repositório (6) PASS — isolamento tenant A/B (leitura, escrita, publish, archive, FK/`CreateFlow` com tenant forjado, RLS escondendo a linha mesmo sem filtro explícito), rascunho otimista (conflito de revisão), publish atômico, snapshots imutáveis (trigger bloqueia até o owner; `omnira_app` sem grant), rollback só move o ponteiro, publish concorrente idempotente (6 goroutines → 1 versão), unicidade de slug/default, archive final e idempotente.
- Suítes existentes dependentes de `conversations`/`messages` com as migrations aplicadas: `platform/db` (inclui `TestRLSCompleteness` e `TestRLSPolicyCoverage` sobre as tabelas novas), `conversations`, `messages`, `inbox`, `routing`, `contacts`, `tickets`, `accounts`, `identity`, `channels`, `dashboard`: todas **ok**.
- `scripts/test-migration-roundtrip.sh 000084_...`: PASS (schema idêntico após down/up). O script só reverte a migration mais recente; `000082`/`000083` foram revertidas e reaplicadas manualmente em banco descartável (sem erro).
- `gofmt -l internal/flows` limpo; `go vet ./internal/flows/...` limpo.

## Histórico de correções desta sessão (transparência)
Um primeiro scaffold foi escrito sem compilar (módulo errado, RLS fora do padrão, `apps/web` duplicado) e suas mensagens de commit afirmavam testes que não foram rodados. Foi **revertido e descartado** (`feat/flow-builder-discarded-scaffold`, backup em `backup/flow-builder-before-rebuild-*` e em `git bundle` fora do repo). Tudo abaixo é refeito com compilação e testes reais.

## Limitações conhecidas (atualizar)
- Nenhuma migration aplicada em banco vivo; nenhuma imagem/stack reconstruída; nada publicado, tagueado ou mesclado.
