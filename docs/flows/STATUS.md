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
| 4 | Nodes determinísticos + SystemSender + efeitos reais | **feita** (gancho de ingest, worker e varredura: fase 4b) |
| 4b | Integração: gancho no ingest, consumidor do worker, varredura de timeouts/encalhadas, compose | **feita** |
| 5 | Simulador (spec FLOW.6) | **feita** |
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

**FLOW.4** (nodes + efeitos reais + envio do bot):
- Catálogo completo executável (17 tipos; teste de paridade catálogo × executores): + resolve_contact (unknown ≠ novo cliente), resolve_customer_context (0 → `none`, 1 → seleciona e grava no **run**, **N → nunca escolhe sozinho**; empresa já confirmada e ainda vinculada é mantida, vínculo encerrado não é confiável), customer_choice (menu numerado, tentativas, timeout), find_open_tickets (placeholders não contam), create_ticket, assign_queue, human_handoff (contexto do bot no run: resumo, fila; conversa → `waiting_human`; run → `waiting_human`; bot nunca fala depois). Falha de efeito segue a porta opcional `error` quando ligada; senão falha o run e devolve a conversa às filas (sem retry cego).
- `PostgresEffects`: `ValidateCustomer` (não grava nada) só aceita empresa **com vínculo ativo do contato da própria conversa** (id forjado, de outro contato ou de outro tenant não passa); a empresa escolhida fica no **run** (`flow_runs.active_customer_account_id`) e no **ticket** (`tickets.customer_account_id`), nunca na conversa; `EnsureTicket` adota o placeholder uma única vez, **nunca sobrescreve** ticket real nem de ERP (`tickets_active_conversation_uq` garante 1 ativo); fila/handoff replicam o `RouteNew` (mesmo job `job.routing.assign.v1` para round-robin), handoff sem fila **não reverte** fila já escolhida, fila de outro tenant = erro (não no-op silencioso).
- `SystemSender` (arquivos novos em `internal/messages`, `Sender` humano intocado): só com TenantContext de sistema; mesma regra de canal e janela de 24h da Meta; `sent_by_user_id = NULL`; idempotência pelo índice novo; **não fala sobre operador** (conversa atribuída ⇒ silêncio); a fila leva só referência (sem texto/telefone); mesma chave com outro texto é recusada.
- Testes: 3 de fake para nodes de dados (+ paridade), 5 de integração dos efeitos (isolamento por contato/tenant, vínculo encerrado, ticket idempotente, filas, envio de sistema ponta a ponta e janela Meta) e **1 ponta a ponta com tudo real** (control plane publica → motor → efeitos → banco): 2 mensagens do bot enfileiradas pelo sistema, ticket adotado com assunto interpolado e prioridade, fila NOC, empresa ativa, contexto de handoff, 8 passos auditados, e silêncio depois do handoff e depois do claim. `go test ./internal/flows/... ./internal/messages/... ./internal/platform/db/...`: **58 testes PASS, 0 FAIL**; `go build ./...` ok; `gofmt -l` limpo nos arquivos desta feature.
- Achados durante a escrita dos testes (esquema real do ADR-0018): `contact_account_links.status` é `active|ended` e `source` tem lista fechada; um contato `customer` exige vínculo ativo — os seeds foram ajustados ao modelo, não o contrário.

**FLOW.4b** (integração com o pipeline existente, tudo atrás de `OMNIRA_FLOWS_ENABLED`):
- Gancho opcional no ingest (`inbox/application/inbound.go`, +26 linhas/−1, mesmo idioma de `WithParticipants`/`WithIdentity`): `FlowGate.Engage` decide, para conversa **nova** de contato externo e antes do roteamento padrão, se um flow publicado a retém (se retém, o `RouteNew` é pulado e o flow roteia no handoff ou em qualquer fim); `FlowGate.OnInbound` enfileira o job `job.flow.inbound.v1` **na mesma transação da mensagem** (ids apenas; nunca texto, telefone ou tenant). Duplicata de webhook não chega ao gate. O `Gate` roda tudo em savepoint e engole/loga erro: **nunca derruba nem perde mensagem**, e o padrão sem flow é o roteamento de hoje.
- Worker: consumidor JetStream durável `worker-flows` (o stream já assina `job.>`; nenhuma mudança de política), handler que **ignora o `tenant_id` do envelope** e deriva o tenant da conversa persistida, erros permanentes são `Term` e o resto `Nak` com atraso; reentrega é inócua. `Sweeper` (a cada 15 s): dispara timeouts vencidos, **libera conversas retidas sem run** após 2 min (um start perdido nunca encalha ninguém) e cancela runs de conversas fechadas.
- Cabeamento: `main` da API (+12 linhas, 0 removidas: gate nos dois `InboundService`, WAHA e Meta), `main` do worker (+24, 0 removidas), `docker-compose.yml` (+2 linhas no bloco de ambiente compartilhado; padrão `false`).
- Testes: 4 unitários do gancho (inclui "sem gate = idêntico a antes" e "redelivery não notifica 2×"); gate em Postgres (só retém o que um flow pode atender: respeita tipo de conversa, atribuição, **conflito de identidade aberto** e filtro de linha; job só quando relevante e sem texto; **não envenena a transação do ingest**); handler (job real como o publisher serializa; reentrega 3×; tenant forjado; envelopes malformados); sweeper (timeout, encalhada com carência, conversa fechada); e o **ingest real ponta a ponta com flag ligada e desligada** (conversa nova retida e sem fila → job → bot cumprimenta → resposta → run completo → fila padrão; flag desligada: roteamento padrão imediato, zero job, zero run).
- Bugs reais pegos pelos testes de integração (corrigidos): (1) parâmetro SQL `$4` usado como texto e `uuid` — o gate engoliu o erro e **nada era enfileirado** (por isso o teste afirma o resultado, não só a ausência de erro); (2) `has_unclassified_participants` significa só "contato ainda não classificado" (ADR-0018), não "conflito": eu bloquearia **todo contato novo**; a regra correta é conflito de identidade **aberto** (`identity_resolution_conflicts`), e contato desconhecido **é** atendido pelo bot (spec §5.2/§104).

**Suíte completa do repositório após a 4b (`go test ./...`, banco descartável): 68 pacotes ok, 5 falhos — os MESMOS 5 da baseline** (authn/httpserver/tenancy: testes que não compilam por IAM5; `intelligence` perf; `outbox TestStoreUsesInjectedTransaction`). Antes da correção abaixo havia 2 regressões minhas, pegas por essa rodada:
- `internal/intelligence TestTwoSubjectsOfOneConversationCarryTheirOwnCompanies` falhou com *"the conversation carries no company"*: o ADR-0017/0018 **proíbe coluna de empresa em `conversations`** (uma conversa tem vários assuntos, cada um com sua empresa). Minha coluna `conversations.active_customer_account_id` (migration 000084) violava o invariante. **Removida**; o contexto de empresa vive no run e no ticket; a empresa já confirmada de uma conversa é reaproveitada lendo o **último run** dela. O teste voltou a passar.
- `internal/iam3 TestSystemRolePermissionMatrix` falhou porque a matriz de papéis é **fixada por teste** e a migration 000083 adiciona 9 chaves. Atualizei a matriz (admin: 9 chaves; supervisor: `flow.view/test`, `flow_run.view`, `flow_template.view`; agent: nenhuma) — mudança deliberada, de 4 linhas, documentada em CONFLICT-ANALYSIS.

**FLOW.6** (simulador, `POST .../flows/{id}/simulate`, permissão `flow.test`):
- Usa o **motor real e todos os executores** sobre repositório **em memória** e efeitos que só registram: não existe conexão de escrita no simulador. Cenário opcional: contato (nome/tipo), provedor (WAHA / Meta) e **janela de 24 h aberta ou fechada**, empresas vinculadas (0/1/N), tickets abertos, relógio (horário comercial), e eventos `message`/`timeout` (limites: 30 eventos, 10 empresas, 4096 caracteres). Devolve passos (porta, saída redigida), mensagens que **seriam** enviadas, efeitos que **aconteceriam** (ticket, fila, handoff, empresa validada, com o passo responsável), variáveis (sem estado privado e sem segredos) e onde ficou esperando.
- Definição com erro bloqueante (inclusive fila de outro tenant) volta `blocked` com as `issues` e **nada roda**. Simula também uma definição ainda não salva do editor.
- Provas: **7 testes unitários/integração + 1 de API**; o de isolamento conta as linhas de 10 tabelas (`flow_runs`, `flow_node_executions`, `messages`, `outbox_events`, `tickets`, `conversations`, `queues`, `contacts`, `flow_versions`, `audit_events`) antes e depois de simular um flow que **executa** send_message, create_ticket, assign_queue e human_handoff: nenhuma muda, e a conversa real continua `bot`. Agente recebe 403, supervisor e admin simulam, tenant B recebe 404 no flow do A, cenário inválido é 400.
- Contrato: `simulateFlow` em `flows-v1.yaml` (guarda de deriva passa).

## Histórico de correções desta sessão (transparência)
Um primeiro scaffold foi escrito sem compilar (módulo errado, RLS fora do padrão, `apps/web` duplicado) e suas mensagens de commit afirmavam testes que não foram rodados. Foi **revertido e descartado** (`feat/flow-builder-discarded-scaffold`, backup em `backup/flow-builder-before-rebuild-*` e em `git bundle` fora do repo). Tudo abaixo é refeito com compilação e testes reais.

## Limitações conhecidas (atualizar)
- Nenhuma migration aplicada em banco vivo; nenhuma imagem/stack reconstruída; nada publicado, tagueado ou mesclado.
