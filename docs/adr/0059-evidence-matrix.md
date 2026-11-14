# ADR-0059 — matriz de evidências (Gates 1-5)

Consolida os testes reais executados em cada fatia do Smart Group Organizer. Vocabulário de
honestidade conforme `RESPOSTA-ESPERADA-DO-CLAUDE.md` do pacote de especificação original:
`IMPLEMENTED`, `UNIT VERIFIED`, `POSTGRES VERIFIED`, `HTTP VERIFIED`, `E2E VERIFIED`, `BLOCKED`.

## Gate 1 — schema + RLS (commit `ddfcbf5`)

| Evidência | Comando | Resultado |
|---|---|---|
| Build/vet/gofmt | `go build ./...` / `go vet ./...` / `gofmt -l` | limpo |
| RLS adversarial, Postgres real | `scripts/test-integration-one.sh ./internal/hub/adapters TestHubGroupOrganizerRLS` | 7/7 ok |
| Reversibilidade da migration | ciclo manual `up -> down 000183_hub_group_organizer -> up` em Postgres descartável | ok |
| Regressão completa do pacote | `scripts/test-integration-one.sh ./internal/hub/adapters` (suíte inteira) | só `TestDelegatedRealtimeStreams`, confirmado pré-existente via `git stash` |

Status: **POSTGRES VERIFIED**. Sem API nem UI nesta fatia.

## Gate 2 — API HTTP de escrita (commit `496e825`)

| Evidência | Comando | Resultado |
|---|---|---|
| Build/vet/gofmt | idem | limpo |
| Testes HTTP reais, Postgres real | `scripts/test-integration-one.sh ./internal/hub/adapters TestGroupOrganizerAPI` | 7/7 ok |
| RLS do Gate 1 intacta | `scripts/test-integration-one.sh ./internal/hub/adapters TestHubGroupOrganizerRLS` | 7/7 ok |
| Reversibilidade da migration 000186 | ciclo manual `up -> down -> up` em Postgres descartável | ok |
| Drift OpenAPI ↔ rota real | `go test ./internal/platform/httpserver/... -run TestEveryDocumentedOperationIsARegisteredRoute` | nenhum path do Gate 2 ficou de fora |
| Regressão completa | suíte inteira de `internal/hub/adapters` | só a mesma falha pré-existente |

Status: **HTTP VERIFIED**. Flag `OMNIRA_GROUP_ORGANIZER_ENABLED` OFF por padrão.

## Gate 3 — UI (commit `4084921`)

| Evidência | Comando | Resultado |
|---|---|---|
| Build/vet/gofmt (backend) | idem | limpo |
| Postgres real (novo `can_manage_groups` em `GET /hubs`) | `scripts/test-integration-one.sh ./internal/hub/adapters` | sem regressão nova |
| Vitest dos arquivos tocados | 33 testes | verdes |
| Vitest suíte completa | 1221 testes | sem regressão nova (2 flakies de ordem, confirmados pré-existentes) |
| Playwright, navegador real | `test:e2e:local` (build fresco) | 4/4 verdes, incluindo a prova de que nenhum elemento é `draggable` (D-12) |

Status: **E2E VERIFIED** para a tela em si (lista/pastas/Kanban, sem DnD); backend seguia **POSTGRES VERIFIED** desde os Gates 1-2.

## Gate 4 — resolvedor de contexto (commit `e3c3e87`)

| Evidência | Comando | Resultado |
|---|---|---|
| Build/vet/gofmt | idem | limpo |
| Teste HTTP real, Postgres real | `scripts/test-integration-one.sh ./internal/hub/adapters TestGroupOrganizerAPI_ResolveContext` | ok — cobre `resolved`, `ambiguous`, `no_access`, invisibilidade de tenant não autorizado, grupo inexistente |
| Regressão completa | suíte inteira de `internal/hub/adapters` | só a mesma falha pré-existente |
| Drift OpenAPI ↔ rota real | `TestEveryDocumentedOperationIsARegisteredRoute` | nenhum drift novo; a única falha (`group-sla-alert`) é pré-existente e não relacionada (confirmada via `git stash`) |

Status: **HTTP VERIFIED**. Nenhuma migration nova; nenhum efeito colateral; não integrado a nenhum motor de ticket/SLA/fila.

## Gate 5 — hardening (commit que acompanha este arquivo — ver `git log -1 -- docs/adr/0059-evidence-matrix.md`)

| Evidência | Comando | Resultado |
|---|---|---|
| Achado de telemetria corrigido | revisão manual dos `audit()` calls de `internal/hub/grouporganizer/service.go` | `UpsertCatalogEntry` gravava `provider_group_id` (JID do WhatsApp, que em grupos antigos embute um número de telefone) na metadata do `audit_events`; removido — o `resource_id` já aponta para a linha completa do catálogo sob RLS, então nada se perde |
| Métricas novas, cardinalidade fechada | `internal/platform/opsmetrics` (mesmo padrão de `internal/hub/adapters/events.go`) | `omnira_hub_group_organizer_writes_total{op=...}` e `omnira_hub_group_organizer_resolve_context_total{status=...}` — nenhum valor de label vem de hub_id/tenant_id/group_id/JID, só nomes de operação fixos no código |
| Build/vet/gofmt | idem | limpo |
| Regressão completa final | suíte inteira de `internal/hub/adapters` (Postgres real) | só `TestDelegatedRealtimeStreams`, pré-existente |

Status: **POSTGRES VERIFIED**. Nenhuma flag foi ligada em nenhum ambiente; nenhum deploy foi feito.

## Falha pré-existente, não relacionada, nunca mascarada

`TestDelegatedRealtimeStreams` (dois subtestes de streams SSE delegados) falha de forma idêntica desde
antes do Gate 1 — confirmado repetidamente via `git stash --include-untracked` contra o baseline em cada
gate. Não é causada por nenhuma linha deste trabalho. Merece investigação própria, fora do escopo desta
ADR; os logs mostram um padrão recorrente de `inbox realtime: closing delegated stream, authorization
lost` que pode indicar um problema real de TTL/cache no caminho de realtime delegado, não apenas
lentidão de teste.

De forma semelhante, `TestEveryDocumentedOperationIsARegisteredRoute` tem uma falha pré-existente e não
relacionada (`GET`/`PUT /api/v1/platform/group-sla-alert` documentados mas não roteados, de uma feature
anterior — ADR-0058 addendum), também confirmada idêntica no baseline via `git stash`.
