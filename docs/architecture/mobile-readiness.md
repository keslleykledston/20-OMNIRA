# OMNIRA — Mobile Readiness (Web + Android + iOS)

**Status:** auditoria concluída em 2026-10-07, base `eb1b2c6` (schema `000089`), branch `feat/mobile-readiness`.
**Escopo:** preparar o OMNIRA Core para ter Android e iOS como *novos clientes*. **Não** constrói o aplicativo.
**Decisões formais:** [ADR-0021](../adr/0021-mobile-client-architecture.md) (arquitetura do cliente e segurança),
[ADR-0022](../adr/0022-authentication-web-vs-mobile.md) (autenticação Web × Mobile),
[ADR-0023](../adr/0023-realtime-events-and-notification-architecture.md) (eventos em tempo real e notificações).

> Pergunta que guia tudo: *se amanhã adicionarmos um app Android ou iOS, ele opera o OMNIRA usando contratos seguros do Core,
> sem reproduzir regras do frontend Web?* **Resposta medida: sim para o domínio e a API; não ainda para três coisas
> (credencial nativa, push, anexo de saída), que são trabalho de fases MOBILE.x e não exigem refazer o Core.**

## 1. Veredito em uma tabela

| Área | Estado | Evidência |
|---|---|---|
| Regras de negócio no backend (Web é cliente fino) | **READY** | §3; `web/src` só chama `/api/v1`, sem serviço externo; permissões vêm de `GET /me/access` |
| Isolamento de tenant (todo cliente, mesma porta) | **READY** | §4; cadeia `authn → membership/RLS` em toda rota; teste de superfície novo (§8) |
| API REST versionada (`/api/v1`), paginação por cursor, idempotência de envio | **READY** | `Idempotency-Key` em `POST …/messages`; cursor opaco em listas |
| Contratos | **PARTIAL** | 3 OpenAPI + 1 AsyncAPI escritos à mão, com teste de deriva; sem cliente/tipos gerados |
| Autenticação | **PARTIAL** | Web = cookie de sessão opaco (revogável). Bearer (ID Token do IdP) já é aceito, mas sem dispositivo, refresh nem revogação por aparelho → ADR-0022 |
| Tempo real | **PARTIAL → melhorado** | SSE por tenant/conversa, sem replay (por desenho). **Agora** com `event_id` e `v` (aditivo) |
| Erros estáveis | **PARTIAL → melhorado** | 743 pontos devolvem `text/plain`. **Agora** `X-Request-ID` em toda resposta e envelope JSON opt-in |
| Anexos (download) | **READY** | `GET …/messages/{id}/media`: tenant, `nosniff`, `Content-Disposition`, Range, pipeline antivírus |
| Anexos (envio pelo agente) | **MISSING** | não existe endpoint de upload de saída (nem para o Web) |
| Notificações / dispositivos | **MISSING** | não há tabela, serviço nem canal; só `LISTEN/NOTIFY` interno do inbox |
| IA | **READY** | tudo no servidor; chaves nunca no cliente; flags preservadas |
| Flow Builder | **READY** | motor no backend, disparado por evento de mensagem; independente de quem originou |
| Papéis de domínio (Tenant, Agente, Contato, Empresa) | **READY** | ADR-0014/0018; `contacts.kind`, `customer_accounts`, N:N contato×conta |
| Observabilidade | **PARTIAL → melhorado** | métricas/OTel existem; `request_id` não existia |
| Testes | **PARTIAL** | suíte ampla com Postgres real; baseline tinha 2 pacotes sem compilar (reparado a montante em `fc12b52`) |
| CI/CD | **UNKNOWN/MISSING** | não há pipeline versionado no repositório (`deploy.sh` manual) |

Legenda: READY = pronto como está; PARTIAL = serve, com lacuna conhecida; MISSING = não existe; BLOCKER = impede a fase seguinte; UNKNOWN = não
foi possível verificar.

## 2. Mapa da arquitetura atual (verificado no código)

```
 Meta Cloud / WAHA ──webhook──► API (Go, monólito modular) ──outbox──► NATS JetStream ──► Worker (Go)
                                   │  /api/v1 REST + SSE                     │ routing, entrega, mídia, fluxos, IA
 Web (React/Vite) ─cookie sessão──►│                                         │
 (futuro) Android / iOS ─Bearer───►│  authn → TenantContext(membership) → RLS → PostgreSQL (fonte de verdade)
                                   └── LISTEN/NOTIFY ─► bridge ─► NATS (inbox.events.{tenant}.{conversa}) ─► SSE
```

- **Backend:** Go, monólito modular (`internal/*`: accounts, ai, attendance, channels, contacts, conversations, flows, inbox, intelligence, media,
  messages, presence, routing, tenancy, tickets…). `apps/api` e `apps/worker`.
- **Dados:** PostgreSQL com RLS `ENABLE+FORCE`; `omnira_app` sem BYPASSRLS; 89 migrations.
- **Mensageria:** NATS JetStream (jobs) + outbox; Valkey só para presença.
- **Frontend:** React 18 + Vite + Tailwind + react-query; nginx do contêiner `web` serve a SPA e faz proxy de `/api` e do webhook da Meta.
- **Auth:** OIDC Code+PKCE (Keycloak) no navegador → cookie `omnira_session` (id opaco em `auth_sessions`); Bearer JWT aceito por `WebMiddleware`.
- **Tempo real:** SSE a partir de NATS, alimentado por triggers de linha → `NOTIFY` → bridge no worker.

## 3. Fronteira da lógica de negócio (MOBILE.READINESS.1)

Pesquisado: chamadas a serviço externo no `web/src`, autorização só visual, filtros de segurança no cliente, estado que deveria viver no servidor.

| Achado | Classe | Situação |
|---|---|---|
| Nenhuma chamada direta do Web a provedor externo, banco ou IA (`axios` só para `/api/v1`) | — | confirmado |
| Autorização: a UI usa `GET /tenants/{id}/me/access` para exibir/ocultar, mas **cada rota reautoriza** no servidor (`AuthorizationMiddleware` + permissão no serviço de aplicação) | — | confirmado |
| Cálculos de apresentação no cliente (`inboxModel.waitInfo`, rótulos de papel, `sortChronological`) | P3 | só exibição; limites vêm de `GET /settings/inbox`. Não replicar regra: o app usa os mesmos dados |
| `localStorage` guarda `token` apenas no modo `mock` (dev); em OIDC não há token no JavaScript | P3 | documentado; o app nativo **não** reproduz isso (ADR-0022) |
| Sem endpoint de upload de anexo de saída | P2 (lacuna de produto, igual no Web) | MOBILE.6 |
| Presença (`POST /me/presence/heartbeat`) assume aba aberta; roteamento usa presença | **P1 para o app** | semântica por dispositivo na ADR-0023 §presença; sem código agora |
| Limitador de taxa enxerga só o balde global `anonymous` (10 000/min/processo): o contexto de tenant é criado *depois* dele e com outra chave | P2 | ver §7, item R-2 |
| SSE continua aberto após a sessão/token ser revogado (a cada 30 s rechecagem só de *membership*; vida máxima 30 min) | P2 | ver §7, item R-3 |
| Erros em `text/plain` (743 pontos) sem código estável | P1 → **corrigido** (opt-in) | §6, `requestmeta.go` |
| Evento em tempo real sem id/versão | P1 → **corrigido** (aditivo) | §6, `sse.go` / `bridge.go` |
| Credencial nativa (cliente OIDC próprio, audiência, renovação, aparelhos) | **P1 de fase** | ADR-0022 (decisões que dependem do dono) |

**P0 (segurança/isolamento): nenhum encontrado.** Provas: cadeia de middleware uniforme (§4) e o teste de superfície que falha se qualquer
rota documentada responder sem identidade verificada (mutação: remover o middleware de uma rota → o teste falha).

### 3.1 Ciclo da conversa, arquivos, IA e fluxos — o Web é consumidor, não dono

| Etapa / área | Quem é dono (backend) | Evidência |
|---|---|---|
| Webhook do provedor → normalização → contato/conversa/mensagem | `channels/*` (WAHA, Meta) + `inbox/application/inbound.go`; a conexão resolve o tenant, nunca o corpo | `TestHandlerIntakeUsesConnectionTenantAndDedupes`, `TestPostgresMetaResolverAndWebhookDedupe`; `channel_webhook_events` com `UNIQUE(connection_id, provider_event_id)` (idempotência do webhook) |
| Roteamento / atribuição / transferência | `routing` + worker (`job.routing.assign.v1`, varredura de vivacidade) | testes de `routing/adapters` (7 de authz/isolamento) |
| Realtime | triggers → `NOTIFY` → bridge → NATS → SSE | §6; o Web consome por `fetch` com `Authorization` e refaz *fetch* ao reconectar (`useRealtimeEvents.ts`): é exatamente o padrão que o app deve repetir |
| Resposta ao cliente | `messages/application/send.go` (permissão `conversation.claim`, janela de 24 h, conversa finalizada = 409, `Idempotency-Key`) → fila → `worker/delivery` → provedor | `send_test`, `send_window_test`, `choice_send_test` |
| Fim do atendimento | `attendance` (transação sob a mesma trava do motor de fluxos) | ADR-0020 |
| Arquivos | pipeline `media` no worker (quarentena → antivírus → liberado); a API só serve arquivo liberado, com `nosniff`, `Content-Disposition`, `ETag`/Range e tenant | `media_retrieval_test`; sem upload de saída (R-4) |
| IA | `ai`, `intelligence`, `aiusage`: provedor por variável de ambiente no servidor (OpenAI ou Gemini) ou chave **por tenant** cifrada (visão); flags `OMNIRA_AI_ENABLED`, `OMNIRA_COPILOT_ENABLED`, `OMNIRA_FLOWS_AI_ENABLED` etc.; contexto não confiável isolado; custo e tokens em `ai_usage` | ADR-0016/0017; este trabalho **não liga nem desliga** nenhuma flag |
| Flow Builder | `flows` (motor transacional por conversa, disparado pelo evento de mensagem recebida no ingest); só edição é Web/Desktop | ADR-0019; uma mensagem de cliente entra igual, venha de onde vier |

Conclusão: nenhuma etapa do ciclo depende do navegador; um cliente novo só chama as rotas da matriz (§5) e escuta o fluxo de eventos.

## 4. Autenticação, tenant e papéis (MOBILE.READINESS.3/4/8)

- **Identidade ≠ autorização.** `authn.Principal{UserID}` vem de (a) cookie opaco resolvido em `auth_sessions` (revogado/expirado → 401; JWT colocado no
  cookie é recusado, sem fallback) ou (b) `Authorization: Bearer` verificado criptograficamente (`OIDCAuthenticator`: RS256, issuer, audiência, expiração,
  identidade `issuer+sub` já provisionada). O `tenant_id` da URL **nunca** autoriza: `AuthorizationMiddleware` abre sessão de banco com o usuário,
  resolve *membership* ativa e injeta `TenantContext`; sem membership → 404 (sem oráculo de enumeração). RLS repete a barreira no banco.
- **Pontos já testados** (suíte `authn`, 44 testes verdes quando o pacote compila): cookie HttpOnly aceito, sessão expirada/revogada recusada,
  *replay* de sessão revogada falha, Bearer JWT continua funcionando, fixação de sessão, nonce OIDC, logout com `end_session_endpoint`.
- **Papéis de domínio preservados** (ADR-0014/0018): Tenant (organização), Agente = `User`+membership (nunca contato), `contacts.kind`
  (`customer|other|spam|agent`; `other` = não classificado), `customer_accounts` + vínculo N:N contato×conta (um contato em várias empresas),
  conversa interna distinguida. Nenhuma mudança deste trabalho toca esses conceitos.
- **Lacuna única de autenticação para mobile** = ADR-0022 (cliente OIDC nativo, audiência por cliente, renovação, revogação por aparelho). Não implementada
  aqui por decisão do escopo e porque envolve decisão de segurança do dono.

## 5. Matriz de capacidades da API (MOBILE.READINESS.2)

Cada linha foi conferida em rota registrada + serviço de aplicação. "Autz" = onde a autorização acontece. Todos exigem identidade verificada e membership
no tenant (§4); a coluna mostra o que mais é exigido.

| Capacidade | Rota (sob `/api/v1/tenants/{t}`) | Web | Mobile-ready | Autz adicional | Testes (exemplos) |
|---|---|---|---|---|---|
| Listar conversas | `GET /inbox/conversations?status=open\|closed\|all&cursor` | sim | **sim** | membership; RLS | `conversation_list_test`, `conversation_status_filter_test` |
| Abrir conversa / mensagens | `GET /inbox/conversations/{c}`, `…/messages?cursor` | sim | **sim** | idem | `conversation_get_test`, `postgres_test` |
| Enviar texto | `POST /inbox/conversations/{c}/messages` (`Idempotency-Key`) | sim | **sim** | `conversation.claim`, dono/co-atendente; janela 24 h | `send_test`, `send_window_test` |
| Enviar template Meta | `POST …/template` | sim | **sim** | idem | `template_send_test` |
| Atribuir / desatribuir | `POST …/assign`, `…/unassign` | sim | **sim** | `conversation.claim`/`manage` | `routing/adapters` (7 testes de authz) |
| Transferir | `POST …/transfer` | sim | **sim** | `conversation.manage` | idem |
| Co-atendimento | `…/invite`, `accept-invite`, `reject-invite`, `leave` | sim | **sim** | `manage`; alvo com `claim` | `participant` tests |
| Finalizar (fechar) | `POST …/finalize`, `…/finalize/suggest` | sim | **sim** | `claim`/`manage` (dono ou supervisor) | `attendance_integration_test`, `http_integration_test` |
| Reabrir | — | — | **n/a por desenho** | próxima mensagem do contato cria nova conversa (ADR-0020) | — |
| Anexo recebido (download) | `GET /messages/{m}/media` | sim | **sim** | tenant; só arquivo liberado pelo antivírus | `media_retrieval_test` |
| Anexo de saída (upload) | — | **não** | **MISSING** | — | MOBILE.6 |
| Contatos (ver/editar) | `GET/PATCH /contacts/{c}`, `PUT …/details` | sim | **sim** | RLS | `contacts/adapters` (13) |
| Classificar contato | `PUT /contacts/{c}/classification` | sim | **sim** | `contact.classify` | `classification_*_test` |
| Empresas / contas do contato | `POST /contacts/{c}/accounts` (+ `end`, `primary`) | sim | **sim** | `account.manage` | `accounts/adapters` |
| Notas, histórico, tickets do contato | `…/notes`, `…/conversations`, `…/tickets`, `…/attendance-history` | sim | **sim** | `account.read`/`ticket.read` | idem |
| Tickets | `…/conversations/{c}/ticket` (+ `status`, `close`, `refresh`), `GET /tickets` | sim | **sim** | `ticket.*`; `Idempotency-Key` no status | `tickets/adapters` (20) |
| Resumo por IA | `POST …/conversations/{c}/ai/summary` | sim | **sim** | membership; sinalizado `OMNIRA_AI_ENABLED` | `ai/adapters` |
| Copiloto / sugerir fechamento | `POST …/topics/{id}/copilot/suggest-reply`, `…/finalize/suggest` | sim | **sim** | `topic.read`/`claim`; flag do copiloto | `intelligence/adapters` (13) |
| Assuntos (topics) | `…/topics/*` | sim | **sim** | `topic.read`/`topic.manage` | idem |
| Execuções de fluxo (leitura) | `GET /flow-runs`, `/flow-runs/{id}` | sim | **sim** | `flow_run.view` | `flows/adapters` |
| Editar fluxos | `…/flows/*` | sim | **não previsto** (Web/Desktop) | `flow.*` | — |
| Permissões do usuário | `GET /me/access` | sim | **sim** | membership | `tenancy/adapters` |
| Tenants do usuário | `GET /api/v1/tenants` | sim | **sim** | identidade | idem |
| Presença | `POST /me/presence/heartbeat` | sim | **parcial** | self | ADR-0010 + ADR-0023 |
| Notificações / dispositivos | — | **não** | **MISSING** | — | MOBILE.9 |

Não foram criados endpoints só para preencher a tabela.

## 6. O que este trabalho mudou (e por quê)

Somente aditivo e opt-in; o Web continua byte a byte igual.

1. **`X-Request-ID` em toda resposta** (`internal/platform/httpserver/requestmeta.go`). Aceita o id do cliente quando é um token simples
   (8–64 de `[A-Za-z0-9._-]`; qualquer outra coisa é descartada — sem injeção em log/cabeçalho), senão gera um UUID. Disponível ao handler via
   `RequestIDFromContext`.
2. **Envelope de erro estável, opt-in.** Com `Accept: application/vnd.omnira.v1+json`, as respostas de erro que seriam `text/plain` viram
   `{"error":{"code","message","request_id"}}`; o `code` deriva do status (`UNAUTHENTICATED`, `FORBIDDEN`, `NOT_FOUND`, `CONFLICT`, `RATE_LIMITED`,
   `UNAVAILABLE`, …) e é a parte estável. Respostas de sucesso, erros que já são JSON e fluxos SSE passam sem alteração (o wrapper preserva `Flush` e
   `http.ResponseController`). Códigos por domínio (`CONVERSATION_NOT_FOUND`) ficam para quando cada rota migrar, sem tocar 743 pontos agora.
3. **Evento em tempo real com `event_id` e `v`** (`sse.go`, `bridge.go`, AsyncAPI). O mesmo evento pode chegar duas vezes (fluxo do tenant + fluxo da
   conversa, reconexão): o cliente deduplica por `event_id`, que também sai como `id:` do SSE. Eventos de um bridge antigo continuam válidos.
4. **Teste de superfície** (`client_surface_test.go`): para **toda** operação documentada sob `/tenants/…` e `/me` (omnira-v1, attendance-v1, flows-v1) exige
   401 para: sem credencial, Bearer lixo, cabeçalho malformado, `Bearer` vazio e tenant/usuário "forjados" por cabeçalho — e nunca pânico antes de
   autenticar. É a rede de segurança para qualquer rota que um cliente novo venha a usar.
5. **Contratos** atualizados (`omnira-v1.yaml`: erros e `ErrorEnvelope`; `contracts/asyncapi/omnira-v1.yaml`: `event_id`, `v`).

Sem migration, sem dependência nova, sem rota nova, sem flag ligada.

## 7. Riscos conhecidos que **não** foram corrigidos (e quando tratar)

| ID | Classe | Descrição | Quando / como |
|---|---|---|---|
| R-1 | P1 de fase | Sem credencial nativa (cliente OIDC próprio, audiência, renovação, aparelho) | MOBILE.1, ADR-0022 — **decisões do dono aprovadas em 2026-10-07**; falta implementar |
| R-2 | P2 | `ratelimit.Middleware` lê `context.Value("tenant_context")` (chave string) mas o contexto do tenant só nasce depois (camada interna) e com `domain.TenantContextKey`: na prática todo tráfego cai no balde `anonymous` (10 000/min por processo, em memória), webhooks inclusos. **Verificado** com teste descartável: cota de tenant = 1/min, 3 requisições com o contexto injetado como o servidor injeta passaram e todas foram contadas no balde `global:anonymous`. Um cliente barulhento pode gerar 429 para todos | Antes do MOBILE.3: aplicar o limite por (tenant, usuário) *depois* do `AuthorizationMiddleware` e isentar `/webhooks/*` do balde global. **Cuidado:** ao ativar, os padrões (100/min/usuário, 1 000/min/tenant) são baixos para o Inbox e estrangulariam o Web; recalibrar com medição antes |
| R-3 | P2 | SSE só reverifica *membership* (30 s); sessão/token revogado mantém o fluxo até 30 min | MOBILE.11 (revogação por aparelho): reverificar a sessão no `recheck` |
| R-4 | P2 | Falta upload de anexo de saída | MOBILE.6 (multipart + URL assinada, mesmo pipeline de antivírus) |
| R-5 | P2 | Presença por aba; app em segundo plano parece offline e sai do roteamento | ADR-0023: estado "disponível no celular" explícito + push, não *heartbeat* |
| R-6 | P3 | Sem CI versionado; sem cliente/tipos gerados da OpenAPI; CSRF só por `SameSite=Lax` (Bearer não usa cookie) | MOBILE.0 (gerar tipos) |
| R-7 | P3 | Suíte Go com falhas anteriores a este trabalho (§9) | tratadas por quem as introduziu |

## 8. Contratos compartilhados (MOBILE.READINESS.11)

**Decisão: não criar `packages/*` agora.** O Web escreve seus tipos à mão (`web/src/types/api.ts`) e a OpenAPI é "escrita à mão a partir da implementação"; gerar um
pacote hoje congelaria um contrato que ainda tem lacunas (erros por domínio, upload). O que existe e basta: três OpenAPI + AsyncAPI **com teste de deriva**
(rota documentada ⇔ rota registrada). No MOBILE.0: gerar tipos/cliente TypeScript a partir dessas especificações (`openapi-typescript`), em
`packages/api-contracts`, consumido pelo Web e pelo app.

| Camada | Compartilhar | Não compartilhar |
|---|---|---|
| `packages/api-contracts` (gerado) | DTOs, enums, códigos de erro, tipos de evento | — |
| `packages/api-client` (fino) | chamadas tipadas, tratamento do envelope de erro, `X-Request-ID`, `Idempotency-Key` | armazenamento de credencial (varia por plataforma) |
| Web only | componentes React/Tailwind, rotas, Flow Builder | — |
| Mobile only | navegação nativa, push, câmera/galeria/áudio, Keychain/Keystore, cache local | regras de negócio |
| Server only | regras, autorização, IA, fluxos, segredos, antivírus, provedores | — |

## 9. Testes e baseline

Comandos e resultados reais no relatório final. Resumo do baseline em `eb1b2c6` (Postgres descartável `omnira_test_mr`, `go test -p 1 ./...`): 72 pacotes
`ok`; falhas **anteriores a este trabalho**: `authn` e `tenancy/adapters` não compilavam os testes (reparado a montante em `fc12b52`; com o reparo, `authn` passa 44/44 e
`tenancy/adapters` tem 1 falha de expectativa antiga: `TestListRolesReturnsFixedPermissionMatrixReadOnly` não conhece as permissões `flow.*`),
`flows/application` (`TestInstallStarterPackCreatesOnlyTenantOwnedDrafts`, Starter v3 em andamento por outro trabalho),
`intelligence/adapters` (`TestQueriesStayFastOnABusyConversation`, teste de desempenho) e `outbox/adapters` (`TestStoreUsesInjectedTransaction`).

## 10. Segurança, offline e observabilidade futuras

Ver ADR-0021 (modelo de segurança do app, política de cache offline e observabilidade) e ADR-0022 (credenciais). Resumo das regras duras:
credencial só em Keychain/Keystore; biometria apenas destrava o segredo local e **nunca** substitui a autorização do servidor; nada de token em log;
o servidor é a fonte da verdade; ação enfileirada offline exige `Idempotency-Key`; cache é apagado no logout/revogação; nenhuma chave de IA no aparelho.

## 11. Roteiro futuro (não implementar agora)

Dependências entre fases: MOBILE.0 → 1 → 2 → (3, 4, 5) → (6, 7, 8) → 9 → (10, 11) → 12 → 13 → 14 → 15 → 16.

| Fase | Entrega | Depende de | Testes / segurança | Aceite |
|---|---|---|---|---|
| MOBILE.0 | Estrutura (`apps/mobile` Expo, `packages/api-contracts`, `packages/api-client`), geração de tipos da OpenAPI, CI com lint/type/test | decisão ADR-0021 | tipos gerados idênticos aos do Web; sem segredo no repositório | app "hello" compila Android/iOS; Web inalterado |
| MOBILE.1 | Autenticação nativa (ADR-0022): cliente OIDC próprio, Code+PKCE, token de acesso curto, renovação, Keychain/Keystore | ADR-0022 aceita; criar o cliente `omnira-mobile` no Keycloak | negativos: token expirado, revogado, audiência errada; nenhum token em log | login, renovação silenciosa e logout revogando no servidor |
| MOBILE.2 | Contexto de tenant/usuário (`/me`, `/tenants`, `/me/access`), troca de tenant | 1 | membership forjada recusada; troca de tenant limpa cache | permissões do app = permissões do Web |
| MOBILE.3 | Inbox (lista, filtros, cursor) | 2; **R-2 resolvido** | isolamento A/B; 429 tratado | lista idêntica à do Web para o mesmo usuário |
| MOBILE.4 | Conversa (mensagens, enviar com `Idempotency-Key`, atribuir/transferir/finalizar) | 3 | reenvio não duplica; ação sem permissão recusada | fluxo completo de atendimento |
| MOBILE.5 | Tempo real (SSE com cabeçalho Bearer, dedupe por `event_id`, *refetch* ao reconectar) | 4 | tenant A não recebe evento de B; reconexão com *backoff* | sem mensagem perdida após queda de rede |
| MOBILE.6 | Anexos: câmera, galeria, documento, áudio; **endpoint de upload de saída** (R-4) | 4 | MIME/tamanho/antivírus, nome saneado, tenant | envio e visualização de mídia |
| MOBILE.7 | Contatos e empresas | 2 | RBAC `contact.classify`/`account.manage` | classificar e vincular empresa |
| MOBILE.8 | Tickets | 4 | idempotência de status | abrir/atualizar/fechar |
| MOBILE.9 | Push (ADR-0023): `user_devices`, `notifications`, decisão e entrega FCM/APNs; presença no celular (R-5) | 1, 5 | token de aparelho nunca em log; revogação | push chega sem expor conteúdo na tela bloqueada |
| MOBILE.10 | Assistência de IA (resumo, sugestão) | 4 | nada de chave no aparelho; PII | mesmos resultados do Web, só servidor |
| MOBILE.11 | Biometria local + gestão de aparelhos (listar/revogar) + revogação no SSE (R-3) | 1, 9 | aparelho perdido → sessão cai em ≤ 30 s | revogar no painel derruba o app |
| MOBILE.12 | Observabilidade e *crash reporting* sem PII | 4 | filtro de campos sensíveis | `request_id` ligado ao relatório |
| MOBILE.13 | Distribuição interna Android | 1–8 | assinatura, ofuscação | APK/AAB interno instalável |
| MOBILE.14 | iOS TestFlight | 13 | *entitlements*, ATS | build no TestFlight |
| MOBILE.15 | Conformidade das lojas (privacidade, LGPD, política de dados) | 13, 14 | revisão jurídica | formulários aprovados |
| MOBILE.16 | Lançamento Google Play / App Store | 15 | *staged rollout* | publicado |

## Revisão independente (Codex, 2026-10-07)
Executada sem o sandbox do Codex (indisponível no host: AppArmor restringe user namespaces) e sobre uma cópia da branch, sem escrita. 0 BLOCKER, 4 HIGH, 4 MEDIUM, 2 LOW.

| # | Sev. | Achado | Classificação | Destino |
|---|---|---|---|---|
| 1 | HIGH | SSE não reverifica a sessão (só membership) | VÁLIDO, anterior a esta branch (R-3), afeta também o Web | ADIADO: especificado na ADR-0022 (SSE guarda a sessão e chama `ResolveSession` no recheck), entra no MOBILE.1 |
| 2 | HIGH | `client_surface_test` só usa chamadas anônimas | PARCIAL: é uma rede de segurança anônima por desenho; o isolamento entre tenants autenticados é coberto por testes de integração de tenancy/inbox com PostgreSQL real | ADIADO: teste com identidade válida fica no MOBILE.1 (precisa do emissor de sessão) |
| 3 | HIGH | ADR-0022 sem rotação atômica do refresh | VÁLIDO | CORRIGIDO na ADR (transação, `FOR UPDATE`, família, teste de corrida) |
| 4 | HIGH | ADR-0022 sem binding OIDC nativo (client_id, redirect, nonce, azp) | VÁLIDO | CORRIGIDO na ADR |
| 5 | MED | Rate limiter global | VÁLIDO = R-2 | ADIADO (recalibrar antes de ativar cotas) |
| 6 | MED | `event_id` muda em redelivery | VÁLIDO como limite; NATS core não redelivera | DOCUMENTADO (ADR-0023) |
| 7 | MED | Opt-in por `Accept` usa substring e ignora `q=0` | VÁLIDO | CORRIGIDO (parser exato + teste) |
| 8 | MED | ADR-0022 sem schema/ciclo de vida de tokens e aparelhos | VÁLIDO | CORRIGIDO na ADR |
| 9 | LOW | Wrapper sem `Hijacker`/`ReaderFrom` | VÁLIDO (nenhum handler usa hoje) | `Hijacker` CORRIGIDO; `ReaderFrom` é só otimização, não implementado |
| 10 | LOW | OpenAPI não referencia `ErrorEnvelope` nas respostas | VÁLIDO | CORRIGIDO (`Text400/401/403/404/502`) |

Do primeiro job do Codex (somente material colado) já tinham sido tratados `Vary: Accept` e respostas 1xx (commit `4356a12`).
