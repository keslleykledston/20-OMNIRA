# 02 — Matriz de impacto

Legenda: **A** = aditivo (arquivo/tabela novos) · **E** = edição pequena em arquivo existente · **—** = não tocado.

## Banco (faixa reservada 000150–000159)

| Migration | Conteúdo | Tipo | Reversão |
|---|---|---|---|
| 000150 | contadores, catálogos (categorias, resultados), `service_sessions`, `service_session_episodes`, permissões `service.*`, RLS, gatilhos de linha | A | `down` remove tabelas e permissões |
| 000151 | `context_events` (append-only, imutável) — **sem** emissão ao outbox | A | `down` remove |
| 000152 | `service_cases`, `service_case_sessions`, `service_tasks` | A | `down` remove |
| 000153 | `external_records`, `service_external_links`, `external_record_requests` | A | `down` remove |
| 000154 | domínio `service` em `permission_domains` (`ALTER` do CHECK) + políticas delegadas do Hub (leitura/escrita) | A (+ `ALTER` do CHECK) | `down` remove políticas e linhas, e volta o CHECK |

Nenhuma migration altera tabela existente além do CHECK de `permission_domains`. Nenhuma toca `messages`, `conversations`, `tickets`. Roll-forward preferido; `down` só em ambiente sem dados (as tabelas são novas, a perda é dos dados novos).

## Go

| Área | Mudança | Tipo |
|---|---|---|
| `internal/servicectx/{domain,application,ports,adapters}` | núcleo novo | A |
| `internal/platform/httpserver/server.go` | `RegisterServiceContextHandlers` (+ marcação `Delegable`) | E (poucas linhas) |
| `internal/platform/config/config.go`, `docker-compose.yml`, `.env.example` | flags `OMNIRA_SERVICE_*` (2 na Fase 1; até 4 ao final) | E |
| `apps/api/cmd/omnira-api/main.go` | monta o serviço | E |
| `internal/attendance/application/service.go` | porta opcional `SessionCloser` chamada em `Finalize` | E (≈10 linhas, nil-safe) |
| `internal/audit/domain/event.go` | constantes de ação/recurso | E |
| `internal/flows/*` | novos nós (Fase 6) | E (procedimento de 9 passos) |
| `internal/intelligence/*` | ferramentas de IA (Fase 6) | E |
| `internal/platform/opsmetrics` | contadores novos | A |
| `internal/worker/*` | nenhuma entrega nova na v1 (eventos ficam em `context_events`; entrega a terceiros = headless) | — |

## Contratos públicos

| Contrato | Impacto |
|---|---|
| `contracts/openapi/omnira-v1.yaml` | **inalterado**; módulo novo em `contracts/openapi/service-context-v1.yaml` (padrão de `flows-v1`/`attendance-v1`) com teste de aderência rota↔spec |
| `contracts/asyncapi/` | novo `service-context-events-v1.yaml` + JSON Schema do envelope |
| APIs existentes (`/finalize`, `/ticket`, `/inbox/*`) | comportamento e resposta idênticos, com as flags desligadas **e** ligadas: o hook fecha a sessão por dentro e não altera o corpo de `/finalize` (a sessão se lê por `GET …/conversations/{id}/service-session`) |
| Webhooks de entrada (WAHA/Meta) | — |

## Frontend

| Área | Mudança |
|---|---|
| `web/src/lib/serviceContext.ts` | cliente novo (A) |
| `web/src/components/service/*` | painel *Atendimento* (protocolo, estado, classificar, encerrar), tarefas, ocorrências, OS externas, timeline (A) |
| `web/src/components/inbox/ContextPane.tsx` + `DelegatedContextPane.tsx` | uma `CollapsibleSection` cada (E) |
| `web/src/lib/permissions.ts` | rótulos das 7 permissões (E) |
| `FinalizeDialog.tsx` | opcional: escolher resultado (E, Fase 5) |

Sem flag no web (padrão do projeto): o painel se oculta quando a API responde 404/501.

## Riscos de regressão e mitigação

| Risco | Mitigação |
|---|---|
| `Finalize` quebrar | porta nil-safe, flag desligada por padrão, teste de regressão com e sem a porta, falha da sessão **dentro de savepoint** não aborta o fechamento da conversa (decisão: finalizar a conversa nunca é bloqueado pelo módulo novo) |
| Contador gerar contenção | trava por tenant só na criação; tx curta; ordem fixa de travas |
| Colisão de migration com headless | faixa 150–159; documentada em `PROGRESS.md` |
| RLS permissivo demais | teste de varredura de catálogo + testes A/B por tabela, incluindo delegado e membership revogada |
| Vazamento por `external_id` | unicidade e consultas sempre com `tenant_id`; teste de cruzamento |
