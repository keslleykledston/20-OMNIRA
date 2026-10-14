# Headless — inventário de baseline (H0)

Base: `main` @ `88e1828` (contém `8becedc` + migration 134). Levantamento por **uso real** no código, em 2026-10-14, somente leitura.
Escopo: quem lê/escreve o "titular" da conversa e tudo que o Headless precisa reaproveitar. Referências `arquivo:linha` valem para `88e1828`.

## 1. O titular hoje

Único campo: `conversations.assigned_to_user_id UUID REFERENCES users(id) ON DELETE SET NULL` (`000015:28`), sem CHECK. `IS NULL` significa
"livre" em **todo** o código (não há outra convenção). Finalizar não limpa o titular (`attendance/adapters/postgres.go:189`).
Campo vizinho: `conversations.automation_mode` (`none|bot|waiting_human|human`, `000084`): "quem fala" = `bot AND assigned_to_user_id IS NULL`.

### Escritores (7 caminhos)
| Onde | Para quê | CAS em NULL? |
|---|---|---|
| `routing/adapters/assign.go:61` (`SetAssignee`) | assign/claim/unassign/transfer manual | **não** (sobrescreve sob `FOR UPDATE` `:44`) |
| `routing/adapters/postgres.go:55,108,279` | claim próprio (sem rota HTTP), rodízio legado | sim |
| `routing/engine/engine.go:836,940,1051` | motor: pickAgent, pickOnCall, tryAssignLocked | sim (`IS NULL AND status='open'`) |
| `hub/replying/service.go:191,330` | claim/transfer/release do Hub | linha travada, checado em Go |
| `hub/distribution/distribute.go:182` | pool do Hub | `FOR UPDATE SKIP LOCKED` |

### Leitores que decidem
- **Autorização:** `messages/application/send.go:143` (sem titular → `ErrUnassigned`; de outro → exige `conversation.manage`); `delegated_send.go:70`; `messages/adapters/postgres.go:127` e `attachment_postgres.go:109` (rechecagem sob `FOR SHARE`); `attendance/application/service.go:202`; `routing/application/assign.go:118,163`; `hub/replying/service.go:113,263`; `inbox/adapters/activity_conversation_postgres.go:36`; `tickets/adapters/conversation_authorizer_postgres.go:34`; `intelligence/adapters/closing_suggest.go:40`, `topic_repository.go:295,311`.
- **Capacidade (conta `status='open'` por usuário):** `routing/adapters/assign.go:130`, `postgres.go:104,243,276`, `routing/engine/engine.go:806,880,887`, `sectors/adapters/eligibility.go:106,134`; Hub conta `status<>'closed'` (`hub/distribution/*`, `replying/service.go:282`).
- **Lista/filtro:** `inbox/adapters/http.go:155,171,243,323`, `contacts/adapters/contact360.go:183`, `hub/adapters/http.go:377`, `hub/adapters/postgres.go:338`, `worker/hubprojector/projector.go:75` (projeta `hub_inbox_items.assigned_user_id`), `sectors/adapters/conversation_routing.go:69`, `intelligence/adapters/routing_repository.go:364`.
- **SLA/timer:** `routing/engine/engine.go:142,191`, `sla.go:338` (titular ⇒ relógios de atribuição/fila cumpridos), `sla.go:346` (1ª resposta só conta `sent_by_user_id IS NOT NULL`), `routing/adapters/liveness_postgres.go:73` + índice parcial `000040` (varredura de paradas), `sectors/adapters/store.go:517`.
- **Flow / bot:** `flows/adapters/gate.go:79,81` (job de entrada condicionado a `IS NULL`), `flows/application/engine.go:131,139` (`controls`, `Startable`), `flows/adapters/postgres_runs.go:30`, **`messages/adapters/outbound_system.go:35`** (bot só fala se `IS NULL`).
- **Outros:** trigger `conversations_realtime_trg` (`000026`) emite `assigned_to_user_id` no NOTIFY; `contacts/adapters/kind.go:187` e `000110:80` (tirar de fila spam, `IS NULL`).
- Nenhum dashboard/relatório/métrica lê o titular (`internal/dashboard` sem ocorrência).

### `assignment_events`
`000015:36`: `from_user_id`, `to_user_id` (sem FK), `changed_by` (FK `users`, anulável desde `000018`), `actor_source` (`human|system`) com CHECK `assignment_events_actor_shape`
(human ⇒ `changed_by NOT NULL`; system ⇒ `NULL`). Append-only (`UPDATE/DELETE` revogados). Nenhum leitor fora de testes. **Um ator "aplicação" não cabe sem mudar o CHECK ⇒ o Headless usa tabela própria tipada.**

## 2. Rotas de controle (tenant e Hub)
| Operação | Rota | Autorização | Trava |
|---|---|---|---|
| assign/claim | `POST …/inbox/conversations/{c}/assign` (`server.go:686`, delegável `conversation.claim`) | `HasPermission` + agente ativo/elegível (`routing/application/assign.go:81-117`), `Source==Direct` | `FOR UPDATE` (`adapters/assign.go:44`) |
| unassign | `POST …/unassign` (`:687`) | próprio: `claim`; de outro: `manage` | `FOR UPDATE` |
| transfer | `POST …/transfer` (`:696`) | `conversation.manage`, alvo com `claim` e elegível | `FOR UPDATE` |
| reply texto | `POST …/inbox/conversations/{c}/messages` (`:705`) | `claim` + titular-ou-`manage` (`send.go:122-148`) | `LoadSendContext` sem trava; `InsertQueued` rechecagem `FOR SHARE` |
| template/anexo | `…/template`, `…/attachments` | idem | `FOR SHARE` |
| finalize | `POST …/inbox/conversations/{c}/finalize` | `claim`/`manage`; dono ou `manage` (`attendance/application/service.go:183`) | `FOR UPDATE` (`LockConversation`) |
| Hub claim/reply/transfer/finalize | `POST /hubs/{h}/inbox/{item}/…` e gêmeas delegadas (`X-Omnira-Acting-As`) | `Authorize` na sessão do agente + sessão de sistema com `has_active_hub_access` rechecado, `FOR SHARE` nas linhas de grant | `FOR UPDATE` |

Idempotência de envio: `Idempotency-Key` (`^[A-Za-z0-9._:-]{8,128}$`), índice `messages_outbound_idempotency_uq (tenant, sent_by_user_id, key)`; mesma chave + corpo diferente = 422. Claim/release sem chave (idempotência natural `Changed:false`). Finalize idempotente pelo fechamento existente.

## 3. Motor de roteamento (ADR-0044)
Ordem de trava: **conversa → `routing_engine_state` → timer** (`engine.go:6`). "Ocupada" = `c.assigned != nil` ⇒ `ReasonAlreadyAssigned` e cancela timers (`engine.go:230`). Todo UPDATE repete `assigned_to_user_id IS NULL AND status='open'` (`:642,836,940,1051`).
Episódio (`routing_engine_state.episode`) incrementa quando a conversa aberta e sem dono reabre; timers carregam `generation` + `claim_token` (fencing, `timers.go:122`).
`routing/overflow`, `routing/eligibility`, `internal/scheduling` são puros (não leem o titular).
**Pontos que precisam enxergar titular não humano:** os ~20 `IS NULL` acima; o ramo `engine.go:230`; a regra de episódio (`:1090`); contagens de capacidade; `sla.go:346`; o CHECK de `assignment_events`; a FK `assigned_to_user_id → users`; a projeção do Hub; o payload do NOTIFY.

## 4. Infra de eventos existente
- `outbox_events` (`000003/21/42`): **só jobs** (`job.>`), publicados por `worker/publisher` (`ORDER BY created_at`, sem trava, JetStream `WithMsgID`). `created_at = now()` (início da transação), **não** é ordem de commit.
- Fatos **sem** evento durável hoje: troca de titular, release, fechamento, status de mensagem, atribuições do motor/legado (sem `audit_events`).
- SSE/NATS (ADR-0023): efêmero; `event_id` nasce no bridge (`bridge.go:208`) — identifica a entrega, não o fato.
- `channel_webhook_events`: dedupe de entrada, sem payload. Não existe `domain_events`.

## 5. Sessão de banco (ADR-0042) e pontos de encaixe
Atores: `Direct`, `Hub`, `HubManage`, `HubServe`, `System` (`tenancy/domain/context.go:36`). Contexto assinado `b1|user|sistema|nonce|hmac` — **sem tenant, sem tipo de ator**.
`WithSystemTenantSession` = `is_system_admin()` no banco; o tenant só existe no `TenantContext` e nos predicados `tenant_id=$1` (**não há ator de sistema preso a um tenant no banco** — risco residual R-1 do threat model).
Tokens opacos reutilizáveis: `authn/device_store.go` (prefixo, 32 bytes, só o SHA-256 guardado, famílias). Criptografia: `channels/adapters/crypto` (AES-256-GCM, `OMNIRA_CREDENTIALS_KEY`). Não existe tabela de API key.
Registro de rotas: `s.mux.Handle("METHOD /api/v1/tenants/{tenant_id}/…", authnMiddleware(tenantSession(h)))`; `delegable_routes_test.go` fixa a lista de rotas delegáveis.

## 6. Migrations e RLS
Última: `000134`; próxima livre: `000135` (livre em todas as branches). Modelos de FORCE RLS: `000127` (laço `DO $$` + políticas membership/sistema + FK composta) e `000130`; `000131` (trigger `forbid_tenant_change`); `000015` (append-only); `000121` (inserts escopados). `internal/platform/db/rls_completeness_test.go` reprova tabela com `tenant_id` sem RLS+FORCE+política.

## 7. Matriz: módulos que migram para o titular canônico
| Módulo | Mudança | Como |
|---|---|---|
| `conversations` | colunas de controle + versão | migration 135 (aditiva) + gatilho funil |
| escritores legados (7) | **sem refatoração**: o gatilho deriva o controle e barra roubo de titular CRM | gatilho |
| `engine.go:230`, liveness, spam dequeue | tratar `control_kind IS NOT NULL` como ocupada | SQL/Go pontual |
| `outbound_system.go`, `flows/*` | bot não fala/inicia em conversa com titular CRM | predicado `control_kind IS NULL` |
| `send.go`, `delegated_send.go`, `assign.go` | erro distinto "mantida por CRM" (409) | Go pontual |
| capacidade (`eligibility`, `engine`, RR) | CRM não consome capacidade de agente | nenhuma (conta só `assigned_to_user_id`) |
| `sla.go:346` | 1ª resposta de aplicação conta | SQL pontual |
| Hub (`hubprojector`, `replying`, `distribution`) | `hub_user` explícito; item mostra titular CRM | Go + SQL pontual |
| `assignment_events` | intocado (humanos) | — |
| eventos | `domain_events` por gatilho | migration 136 |
