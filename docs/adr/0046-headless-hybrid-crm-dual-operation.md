# ADR-0046 — OMNIRA Headless / Híbrido: CRM externo e Workspace sobre um único controle de conversa

Status: **Accepted para implementação local** (2026-10-14). O dono mandou executar H0–H6 em worktree isolado (`feat/headless-crm-dual-operation`), sem push, merge ou deploy.
As decisões abertas do plano (§14 do plano original) foram tomadas pelo critério de **menor risco** e estão marcadas "padrão adotado"; podem ser revistas.
Relaciona: ADR-0013 (ERP autoritativo), ADR-0023 (SSE efêmero), ADR-0037 (escrita do Hub), ADR-0040/0042 (Hub completo, contexto assinado), ADR-0043/0044 (setores, motor de roteamento), ADR-0018 (internas), `docs/architecture/INTEGRATION_CONVERGENCE.md`.
Inventário que fundamenta este texto: `docs/architecture/HEADLESS-INVENTORY.md`. Ameaças: `docs/architecture/HEADLESS-THREAT-MODEL.md`.
Vocabulário de evidência: IMPLEMENTED / NOT WIRED / UNIT / POSTGRES / HTTP / E2E VERIFIED / REAL-PROVIDER BLOCKED / CODEX BLOCKED / BLOCKED.

## 1. Problema

Um CRM externo precisa **ver** as mesmas conversas do Workspace/Hub em tempo real e **agir** (assumir, responder, transferir, finalizar) sem que duas superfícies escrevam ao mesmo tempo.
Hoje o titular é só `conversations.assigned_to_user_id` (FK para `users`); não existe identidade de aplicação, não há journal durável de fatos e o SSE é efêmero (ADR-0023).

## 2. Decisão A × B (controle tipado)

**A — colunas de controle tipado em `conversations`** (escolhida) × **B — tabela `conversation_control` separada.**

| Critério | A (em `conversations`) | B (tabela separada) |
|---|---|---|
| Fontes da verdade | uma linha; `assigned_to_user_id` vira projeção humana mantida por gatilho | duas linhas por conversa (conversa + controle) com FK 1:1; divergência possível entre elas |
| Lock | a trava `FOR UPDATE` da conversa que **todo** caminho já toma cobre o controle | segunda trava (ordem `conversa → controle → estado → timer` muda a ordem documentada do motor; novo risco de deadlock) |
| Escritores legados (7) | **não mudam**: um gatilho `BEFORE` funil deriva o controle | cada um teria de escrever na tabela nova (7 refatorações) ou ganhar um gatilho igual |
| Leitores `IS NULL` (~20) | continuam corretos para humanos; só os que **agem** sobre "livre" ganham `AND control_kind IS NULL` | mesma alteração, mais um JOIN em cada leitura |
| Backfill | uma `UPDATE` sobre conversas com titular | `INSERT` de uma linha por conversa (tabela quente, milhões de linhas no futuro) |
| Custo | colunas largas numa tabela quente (4 colunas pequenas) | JOIN em toda lista do Inbox/Hub |
| Rollback | colunas ficam inertes (aditivo); gatilho pode ser desligado | tabela fica órfã; leitores a ignorar |
| Risco comprovado | menor: um ponto de decisão no banco | maior: três lugares para manter coerentes |

**Decisão:** A, com **funil no banco**.

### 2.1 Modelo (migration 135, aditiva)
Em `conversations`:
- `control_kind TEXT NULL` ∈ `tenant_user | hub_user | crm_application | crm_agent_verified` (NULL = livre);
- `control_holder_id UUID NULL` (usuário, ou `external_app_installations.id` para CRM);
- `control_version BIGINT NOT NULL DEFAULT 0`;
- `control_since TIMESTAMPTZ NULL`;
- `control_origin TEXT NULL` (`workspace|hub|crm|system`), informativo.

`conversation.status` (`open|closed`) continua independente de `control` (`unassigned|held`, derivado de `control_kind IS NULL`).

CHECK: `(control_kind IS NULL) = (control_holder_id IS NULL)`; tipos humanos ⇒ `assigned_to_user_id = control_holder_id`; `crm_application` ⇒ `assigned_to_user_id IS NULL` e FK composta `(tenant_id, control_holder_id)` para a instalação (por gatilho de integridade, pois a FK é condicional ao tipo; ver §2.3).
`crm_agent_verified` existe no domínio e no CHECK, mas **nenhum caminho o produz** (NOT WIRED; sem federação real, §6).

### 2.2 Escritores explícitos + gatilho que DERIVA e VALIDA (revisado após o Codex de H0)
O primeiro desenho descartava a UPDATE do escritor legado (`RETURN NULL`). **Rejeitado**: uma UPDATE que muda `assigned_to_user_id` e `queue_id` perderia a fila junto, `RETURNING` voltaria vazio, e chamadores que ignoram `RowsAffected` (Hub `distribute.go:182`) ou usam fallback (rodízio) registrariam atribuição que não ocorreu. Decisão final:

1. **Cada escritor trata o titular CRM como ocupado, explicitamente**, onde decide:
   - os escritores com CAS (`ClaimUnassigned`, rodízio legado ×2, motor ×3) ganham `AND control_kind IS NULL` ⇒ 0 linhas = "perdeu a corrida", o caminho já testado;
   - `SetAssignee` e o fluxo manual (`routing/application/assign.go`, `participant.go`) leem o controle sob a trava que já tomam e devolvem `409 held_by_external_application` **antes** de escrever (o contrato prometido ao Workspace);
   - o Hub (`replying`, `distribution`) lê o controle sob a trava e recusa; ao escrever, **declara** `control_kind='hub_user'` e `control_holder_id` na mesma UPDATE.
2. **Gatilho `conversations_control_guard` (BEFORE INSERT OR UPDATE)** — nunca descarta; só deriva, valida e, se algo escapar, **falha alto**:
   - *escrita canônica* (a UPDATE muda `control_kind` ou `control_holder_id`): exige `control_version = OLD.control_version + 1` e coerência com `assigned_to_user_id` (CHECK);
   - *escritor legado humano* (muda `assigned_to_user_id` e não toca no controle): deriva `control_kind='tenant_user'`, `control_holder_id`, `control_version+1`, `control_since=now()`; NULL limpa o controle e incrementa a versão;
   - *escritor legado sobre conversa de CRM* (muda `assigned_to_user_id` com `OLD.control_kind='crm_application'` e sem tocar no controle): `RAISE EXCEPTION` (SQLSTATE `OM001`, mensagem `held_by_external_application`). É a rede de segurança; um teste prova que **nenhum escritor conhecido a alcança**, e a exceção grita em vez de corromper em silêncio;
   - conversa fechada não muda de titular por escritor legado (comportamento atual).
   O tipo declarado (`hub_user`) é consistência de aplicação, **não** fronteira de segurança: quem executa SQL com `UPDATE` em `conversations` já pode reatribuir (a RLS é a barreira, ADR-0042).
3. **Realtime:** `conversations_realtime_trg` (`000026`) só dispara em `UPDATE OF assigned_to_user_id, status`; a migration de controle o recria para também disparar em `control_version` e inclui `control_kind` no NOTIFY (aditivo). A projeção do Hub (`hubprojector`) passa a levar o tipo do titular.
Limite honesto: os ~20 leitores `IS NULL` que **agem** sobre "livre" (§2.4) continuam a precisar do predicado explícito; o inventário os lista e o gate H2 tem um teste por leitor.

### 2.3 Serviço canônico `internal/conversationcontrol`
Funções: `Claim`, `Release`, `Transfer`, `Finalize` (via `attendance`), `AuthorizeSend`, `TakeOver` (supervisor). Cada uma, numa transação: trava a conversa `FOR UPDATE` (primeira trava; depois estado do motor; depois timer), **revalida** autorização/estado/instalação ativa (`FOR SHARE` na linha da instalação: a revogação precisa da trava de escrita ⇒ serializa), confere `If-Match`, aplica a transição, incrementa `control_version`, grava `conversation_control_events` (append-only, ator tipado: aplicação, hub, usuário), e deixa o gatilho de eventos emitir o fato.
Reexecução idempotente (mesmo titular, mesmo estado) não incrementa versão nem grava evento.
Nenhuma chamada externa dentro da trava.

### 2.4 Matriz de convivência (decidida)
| Superfície | Comportamento |
|---|---|
| Workspace claim/assign/transfer/unassign | leem o controle; conversa de CRM ⇒ `409 held_by_crm` (corpo estável). Quem tem `conversation.manage` + `conversation.takeover_external` usa `TakeOver` com motivo obrigatório e confirmação explícita; nunca é automático |
| Workspace/Hub reply | `send.go` e `delegated_send.go`: titular CRM ⇒ 409 `held_by_crm` (não `ErrUnassigned`) |
| Hub claim/transfer/distribuição | declaram `hub_user`; o pool não distribui conversa com titular CRM |
| Motor / rodízio legado / liveness | tratam `control_kind IS NOT NULL` como ocupada; wakeups de episódio antigo continuam cancelados pelo epoch existente; o gatilho falha alto diante de qualquer roubo residual |
| Flow Builder / bot | `outbound_system.go`, `flows/*gate*`, `Startable`, `controls`: `AND control_kind IS NULL`. Conversa com titular CRM nunca recebe fala do bot nem inicia fluxo; política de automação sobre conversa CRM **não existe** no MVP (decisão conservadora) |
| SLA | titular CRM cumpre relógios de atribuição/fila; 1ª resposta conta mensagem de aplicação (`sent_by_installation_id IS NOT NULL`) |
| Capacidade de agentes | CRM não consome capacidade de `queue_members` (contagem continua por `assigned_to_user_id`) |
| Conversa interna / spam | nunca exposta ao CRM. Predicado único `external_conversation_visible`: `conversations.internal_user_id IS NULL`, `conversation_kind <> 'internal'` **e** `contacts.kind NOT IN ('internal','spam')` (a derivação de `conversation_kind` mapeia spam para `external_other`, então filtrar só o tipo da conversa vazaria spam); usado por toda leitura, snapshot, evento e mídia |

## 3. Identidade de aplicação (H1)
- `external_app_installations`: **uma por tenant** (um CRM multiempresa = várias instalações, cada uma com credencial própria), `status` ∈ `active|suspended|revoked`, `scopes[]`, allowlist opcional de filas/canais, limites (rate, lease), `created_by`, datas.
- `external_app_credentials`: segredo opaco `omn_ext_<id>.<secret>` (32 bytes de CSPRNG); só `SHA-256(secret)` fica no banco (alta entropia dispensa KDF lento; comparação em tempo constante); exibido uma vez; `expires_at`, `revoked_at`, `last_used_at`; rotação cria a nova antes de revogar a antiga (janela dupla curta configurável).
- **Portas `SECURITY DEFINER` (migration 135), todas com `search_path` fixo, `EXECUTE` só para `omnira_app`, e presas a tenant + instalação (+ credencial onde importa):** `external_credential_lookup(key_id)` (única consulta anterior ao tenant; devolve o digest para comparação em tempo constante no Go), `external_credential_touch(tenant, instalação, credencial)`, `external_recheck(tenant, instalação, credencial)` (trava as duas linhas `FOR SHARE`; a trava pertence à transação do chamador) e `external_installation_allowlists(tenant, instalação)`. O tenant do `Principal` vem **da instalação** no banco, nunca de header, query ou corpo.
- **Sem ramo de sistema nas policies:** as quatro tabelas (`external_app_*`) têm RLS FORCE com policies que exigem `integration.read/manage` de uma **pessoa** (`actor_has_permission`); **não há `is_system_admin()`**. Assim a API externa — que roda em sessão de sistema — não consegue ler, listar nem alterar instalações de nenhum tenant por esquecer um predicado; só alcança esses dados pelas funções acima. `secret_digest` não tem `SELECT` para o papel da aplicação (privilégio de coluna).
- **Execução:** padrão do Hub (ADR-0037): autentica antes e executa em `WithSystemTenantSession` com o tenant da instalação, e **rechecagem dentro da mesma transação** (`external_recheck`: instalação **e** credencial usáveis agora, ambas travadas `FOR SHARE`) em toda mutação. Revogar/suspender (instalação) e revogar credencial tomam trava conflitante ⇒ ou esperam a escrita ou acontecem antes e a escrita é recusada. Não se amplia nenhuma policy de membership; a aplicação não vira usuário, membership nem `is_system_admin()`. O SQL das *tabelas de domínio* (conversas, mensagens, contatos) executado pela API externa continua em sessão de sistema (risco residual **R-1**); fica confinado a `internal/external/adapters`, é varrido por teste que exige predicado `tenant_id` e coberto por testes A/B/C por rota.
- **Administração:** `tenants/{t}/integrations/apps…` com sessão humana (o `authn.WebMiddleware` aplica a checagem de mesma origem às escritas por cookie; teste de POST entre origens na revogação/rotação, H6), permissão nova `integration.manage` (somente `tenant_admin`; **não delegável** ao Hub). Segredos nunca em lista, log ou auditoria.
- **Flags:** `OMNIRA_HEADLESS_ENABLED=false` (rotas externas, workers de entrega e gatilhos de evento no-op); por instalação, `status`.

## 4. Journal durável e webhooks (H4)
- `domain_events (tenant_id, seq, event_id, event_type, version, aggregate_type, aggregate_id, conversation_id, origin, occurred_at, payload)` com `PRIMARY KEY (tenant_id, seq)`. O `event_id` é gerado **na linha**, no commit, e é o que o CRM recebe (diferente do `event_id` de entrega do SSE).
- **Ordem visível sem travas nos escritores (revisado após o Codex de H0).** Alocar `seq` num gatilho (`UPDATE cursor … RETURNING`) prenderia a linha do cursor até o commit, em posição arbitrária da transação de domínio: com a ordem de travas do motor (conversa → estado → timer) e a reclassificação de contato (contato → conversas) isso abre ciclos de deadlock e um hotspot por tenant. **Rejeitado.** Em vez disso, **marca-d'água de transação**:
  1. o gatilho AFTER faz só um `INSERT` em `domain_events` com `xid = pg_current_xact_id()` e `seq NULL` (nenhuma trava além da própria linha);
  2. um **sequenciador** único (worker, `pg_try_advisory_lock`) pega eventos com `seq IS NULL` e `xid < pg_snapshot_xmin(pg_current_snapshot())` — ou seja, quando toda transação com xid menor já terminou (commit ou rollback) —, ordena por `(xid, ord)` e atribui `seq` denso por tenant a partir de `domain_event_cursors` (ele é o único escritor do cursor);
  3. leituras e webhooks só servem linhas com `seq` atribuído. **Nenhum evento novo pode aparecer abaixo de um cursor já servido**, que é a garantia de que o consumidor precisa. Ordem entre transações concorrentes é por xid; a causalidade por agregado vem de `aggregate_version` no envelope (`control_version` da conversa; mensagens ordenam por estado monotônico). Latência = intervalo do sequenciador (≈0,5 s) + a transação de escrita mais longa do banco (monitorada: alerta de lag do sequenciador). Transação somente-leitura sem xid não segura a marca-d'água (pg_dump não atrasa o journal).
- **Retenção × paginação:** o expurgo remove linhas e, na MESMA transação, sobe `domain_event_cursors.min_seq`; remove **só o prefixo formado inteiramente por fatos expirados** (a numeração segue o `xid`, não o relógio: uma transação longa pode ter número baixo e fato recente, e esse fato nunca é apagado por um vizinho mais novo/antigo); uma entrega cujo fato foi expurgado termina como `cancelled/expired`; um cursor `after < min_seq - 1` recebe `410 Gone` com `resync_required`; uma página lida durante o expurgo é consistente (snapshot da consulta) e a seguinte recebe 410.
- Envelope por padrão **só referências** (ids, tipo, versão, origem, `control_version`), sem texto, telefone ou anexo. O CRM busca o conteúdo pela API autenticada.
- `webhook_subscriptions` (HTTPS, tipos, segredo cifrado AES-GCM via `channels/adapters/crypto`, `key_id`, rotação com janela dupla) e `webhook_deliveries` (por assinatura+evento: tentativas, `next_attempt_at`, estado `pending|delivered|dead`, última classe de erro/HTTP). O worker materializa entregas a partir de um cursor por assinatura (`enqueued_through_seq`), pega com `FOR UPDATE SKIP LOCKED`, assina `HMAC-SHA256(secret, "<timestamp>.<raw_body>")` e envia com cabeçalhos `X-Omnira-{Signature,Timestamp,Event-ID,Delivery-ID,Key-ID}`. Backoff exponencial com jitter até 72 h (configurável); depois `dead` (DLQ) e reenvio administrativo auditado.
- **SSRF:** somente `https`, portas 443/8443 (allowlist configurável), resolução DNS **em cada conexão** com verificação do IP dentro do `net.Dialer.Control` (bloqueia loopback, privado, link-local, metadados, ULA, CGNAT, multicast), nenhum redirecionamento, timeouts e limite de corpo. Teste de DNS-rebinding com resolvedor falso.
- **Replay/bootstrap:** `GET /external/sync/checkpoint` devolve `{seq_head, min_seq, retention_days, at}`; o cliente lê o checkpoint **primeiro**, depois pagina o snapshot (várias requisições: não existe transação que atravesse requisições HTTP, então `REPEATABLE READ` não é a garantia) e depois consome `GET /external/events?after=seq_head`. A convergência vem do desenho, não de isolamento: o evento é uma **referência** (id + versão de controle), então um fato já refletido no snapshot e reaplicado pelo replay é inofensivo, e um fato posterior ao checkpoint sempre é reaplicado; testado com dados mudando durante a paginação (`TestBootstrapWhileTheDataMovesRebuildsTheFinalState`). Cada evento é reautorizado **na leitura** (escopo e allowlist da instalação). Cursor abaixo do mínimo retido ⇒ `410 Gone` + `resync_required`. Retenção padrão 30 dias; entrega até 72 h. Cursor forjado não revela tenant (o cursor é um inteiro dentro do tenant da credencial).
- SSE e NATS permanecem como estão (UI), sem papel de journal.

## 5. Escrita externa (H5)
- `external_command_receipts (tenant_id, installation_id, operation, idempotency_key, request_hash, status, response_code, response_body, message_id, created_at)` com `UNIQUE (tenant_id, installation_id, operation, idempotency_key)`. Justificativa para a tabela nova (o plano pedia reuso): `messages_outbound_idempotency_uq` é chaveado por `sent_by_user_id`, e mensagens de aplicação têm remetente NULL, o que colidiria com o índice parcial de sistema `messages_system_idempotency_uq`. A mensagem do CRM grava `idempotency_key = NULL` (fica fora dos dois índices parciais de mensagens, sem colisão com Flow/sistema) e `sent_by_installation_id`; o recibo e a mensagem são inseridos **na mesma transação** (queda entre os dois = nada persistido) e o recibo é a única fonte do contrato. A repetição compara `request_hash` (conversa + corpo canônico) e devolve a resposta gravada ou 422.
- `If-Match: "control-N"` obrigatório em release/transfer/finalize/messages; em `claim`: sem `If-Match` quando a conversa está livre (corrida decidida pela trava); com `If-Match` se o chamador quer travar contra uma versão. Mesmo titular repetindo `claim` ⇒ 200 idempotente.
- Códigos: `412` versão antiga (nada enfileirado), `409` outro titular, `403` escopo, `404` uniforme para invisível/interno/outro tenant, `422` mesma chave com payload diferente, `202` somente `queued`, `429`+`Retry-After`, `application/problem+json` (RFC 9457).
- Envio reaproveita `OutboundStore.InsertQueued` (mensagem + job `job.channel.send_text.v1` numa instrução), com o predicado de titular trocado por "controle = esta instalação e versão = N" sob `FOR SHARE`; janela de 24 h, template, canal pronto, anexos e antivírus são os existentes.
- `external_agent_id` é **metadado não confiável** (`claimed_agent_ref`), gravado no recibo/evento e marcado `unverified`; nunca vira autor.
- Finalizar chama `attendance` (`FinalizeByExternalApp`, mesma lógica de `FinalizeBySystemWithReason`, ator "aplicação"); **não** muda chamado ERP (ADR-0013).

## 6. Decisões padrão adotadas (pontos abertos do plano)
1. **Lease CRM:** opcional por instalação (`lease_seconds` NULL = sem expiração); padrão conservador no piloto: 900 s renovável por `POST …/heartbeat` ou por qualquer comando, grace de 120 s, expiração libera com motivo `lease_expired` e re-kick do roteador. **Titular humano nunca expira.**
2. **Identidade:** técnica única por instalação; `crm_agent_verified` fica definido e **NOT WIRED** (sem OIDC/assertion confiável não há como verificar; fingir seria pior que não ter).
3. **Fora de horário/setor:** claim externo obedece `sector/calendar` por instalação (`allow_outside_hours` falso por padrão) via a mesma regra de elegibilidade (`sectors/adapters/eligibility.go`); nunca bypass global.
4. **Retenção:** journal 30 d; entrega 72 h; ambos configuráveis; LGPD: envelope sem PII.
5. **Takeover de supervisor:** permissão `conversation.takeover_external` (tenant_admin, tenant_supervisor), motivo obrigatório, evento `control_changed` com `reason=supervisor_takeover`, auditoria; nunca automático após conflito.
6. **Revogação/flag OFF com conversas CRM:** a revogação (e o comando `drain`) libera todas as conversas da instalação na **mesma transação** (`control_kind→NULL`, versão+1, evento, kick do roteador); com a flag OFF global, o runbook chama `drain` por instalação antes de desligar; enquanto não drenadas, o gatilho mantém a proteção (sem roubo) e a UI mostra "mantida por aplicação externa".

## 7. Contratos candidatos
- OpenAPI externa **separada**: `contracts/openapi/omnira-external-v1.yaml` (H1: `me`; H3: leitura; H5: escrita; erro `problem+json`). Administração em `omnira-v1.yaml`.
- AsyncAPI `contracts/asyncapi/omnira-external-events-v1.yaml` + JSON Schemas `contracts/schemas/external/*.json` + amostras assinadas verificadas por teste.
- Teste de contrato: percorre as rotas registradas no `mux` e exige correspondência com a OpenAPI externa (remover uma rota ou evento falha — gate 20).

## 8. Plano por fases (migrations 135+, todas aditivas, `.down` seguro)
H1 135/136 (instalações, credenciais, permissões) · H2 137 (controle em conversas + gatilho + eventos de controle + backfill) · H3 sem schema (índices de leitura) · H4 138 (journal, cursor, assinaturas, entregas) · H5 139 (recibos, `sent_by_installation_id`) · H6 sem schema. Números provisórios; a numeração final é fixada no momento de cada fase (livres hoje em todas as branches).

## 9. Consequências e riscos aceitos
- A tabela quente `conversations` ganha 5 colunas e um gatilho BEFORE; medido no gate (carga 30 mil conversas).
- `WithSystemTenantSession` continua sem ator de sistema preso a tenant no banco (R-1) para as tabelas de domínio: mitigado por predicado obrigatório + lint + testes A/B/C + recheck; as tabelas da própria integração ficaram fora do alcance da sessão de sistema (§3). ADR futuro pode introduzir contexto assinado `b2` com tenant.
- O sequenciador é um ponto único por banco (lag monitorado; escala por partição de tenants se preciso) e uma transação de escrita muito longa atrasa os eventos (alerta).
- Rollback: colunas/tabelas inertes com a flag OFF; `drain` + desligar; `.down` das migrations remove tudo que ninguém usa e **recusa** (com aviso) se houver titulares CRM ou entregas pendentes.
- **Não fazemos:** clone de Chatwoot, ERP autoritativo novo, API pública indiscriminada, exactly-once de rede, federação de agente CRM (H7).
