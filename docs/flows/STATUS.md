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
| 2 | Control plane: validador, publicar/rollback, RBAC, HTTP, OpenAPI | **feita** |
| 3 | Runtime: resolver, passo transacional, wait/resume, idempotência, limites | **feita** (gancho de ingest + worker entram na fase 4, com os efeitos reais) |
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

**FLOW.2** (control plane):
- Domínio puro: modelo de definição (parse estrito, limites 200 nós/400 arestas/100 variáveis/1 MiB), catálogo de **17 nodes** (portas, classe de efeito colateral, waits/terminal), validador (nó/aresta duplicados ou inexistentes, porta inválida, porta obrigatória sem destino, trigger ausente/múltiplo, ciclos, variável inexistente/reservada, segredo embutido, placeholder não resolvido, órfãos como aviso), avaliação determinística de condições, interpolação `{{var}}`, horário comercial com fuso, redação de segredos. 13 testes; **teste de mutação** (desligar detecção de ciclo/segredo) faz os testes falharem.
- Aplicação (`ControlPlane`): criar, rascunho com validação ao vivo, validar com checagem de recursos **no tenant** (fila de outro tenant = `resource_not_found`), publicar (pin de subflows, profundidade ≤ 3, auto-chamada recusada), rollback, archive, settings (linha de canal precisa ser do tenant), auditoria (`flow.*`).
- HTTP + RBAC: 13 operações, permissão por chave (`flow.view|create|edit|test|publish|archive`); testes com admin/supervisor/agent: agent 403 até para listar, supervisor só leitura, **editar ≠ publicar**, tenant B recebe 404 em flow do A e 403 na URL de A, 401 sem sessão, 409 em revisão velha/slug repetido, 422 com `issues` acionáveis.
- Contrato: `contracts/openapi/flows-v1.yaml` (YAML válido, refs resolvidas) + guarda de deriva nos dois sentidos (`contract_test.go`). Ficou em arquivo próprio porque o guarda genérico de `omnira-v1.yaml` (httpserver/contract_test.go, hoje não compila por IAM5) exige registrar toda operação documentada no servidor de teste; ao unificar, basta mover os paths e registrar `RegisterFlowHandlers` lá.
- Cabeamento mínimo atrás de `OMNIRA_FLOWS_ENABLED` (padrão `false`): `config.go` (+2 linhas), `server.go` (função nova `RegisterFlowHandlers`), `main.go` da API (1 bloco). `go build ./...` = ok. `gofmt -l internal/flows` limpo (os 3 arquivos compartilhados já estavam fora do gofmt no HEAD e não foram reformatados).

**FLOW.3** (runtime):
- `Engine` (um evento = um passo transacional, no `WithSystemTenantSession` do worker): trava a conversa (`SELECT … FOR UPDATE`), reconhece evento duplicado por `trigger_event_id`/`last_event_id` (mesmo depois do fim do run), retoma run em espera, inicia run só quando permitido (`conversation_kind` ∈ customer_service/unclassified, aberta, sem responsável, contato presente, sem conflito de identidade, conversa nova ou `restart_policy=always`), respeita prioridade e filtro de canal (`connection_ids`/`providers`), executa nodes até esperar/entregar/terminar/falhar, limita (`max_node_executions`), empilha subflows (versão fixada, profundidade ≤ 3) e **nunca deixa a conversa encalhada**: qualquer fim sem handoff devolve ao fluxo normal de filas.
- Autoridade derivada: o bot só fala se `automation_mode='bot'` **e** sem responsável; operador assumiu ⇒ bot cala e o run é cancelado.
- Nodes executáveis nesta fase: trigger, send_message, ask (validação none/number/email/phone, tentativas), choice (menu numerado; número/rótulo/id; `other`), condition, switch, set_variable, business_hours (fuso IANA), subflow, end. Resposta inválida re-pergunta e esgota para a porta `timeout`; janela de 24h fechada segue a porta `window_closed` ou falha o run de forma limpa.
- Postgres: `LoadConversation` (lock), runs, execuções (append-only, **redigidas** antes de gravar), `CandidateFlows` (específicos por prioridade, default por último), `DueRuns` (timeouts, varredura de sistema).
- Testes: 14 de motor com fakes (completar, ask/resume, duplicata, inválida→timeout, timeout, choice, humano assume, waiting_human, limites/porta solta/erro de executor, janela, condições de início, resolver, subflow, segredos) + 7 de integração em Postgres real (retomar entre transações; **8+8 entregas concorrentes** da mesma conversa → 1 retomada, 1 run, mensagens exatamente uma vez, passos únicos; 1 run ativo/conversa e evento único no banco; isolamento A/B de runs; run fica na versão fixada enquanto outra é publicada; timeout via banco; trilha sem segredo). **Mutação**: remover o `FOR UPDATE` faz o teste de concorrência falhar (violação de `flow_node_executions_flow_run_id_seq_key`); com ele passa 5/5 e com `-race`.
- Achado e corrigido durante os testes: duplicata da **resposta** só era reconhecida com o run ativo; com `restart_policy=always` uma reentrega após o fim reiniciaria o bot. Agora `RunByEvent` consulta trigger e último evento (índice `flow_runs_last_event_idx`).

## Histórico de correções desta sessão (transparência)
Um primeiro scaffold foi escrito sem compilar (módulo errado, RLS fora do padrão, `apps/web` duplicado) e suas mensagens de commit afirmavam testes que não foram rodados. Foi **revertido e descartado** (`feat/flow-builder-discarded-scaffold`, backup em `backup/flow-builder-before-rebuild-*` e em `git bundle` fora do repo). Tudo abaixo é refeito com compilação e testes reais.

## Limitações conhecidas (atualizar)
- Nenhuma migration aplicada em banco vivo; nenhuma imagem/stack reconstruída; nada publicado, tagueado ou mesclado.
