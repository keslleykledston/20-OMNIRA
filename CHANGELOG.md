# Changelog

## Unreleased

### Added (API externa — leitura de grupos do WhatsApp, ADR-0051, pendência CRM §3.1/§3.2)
- `GET /api/v1/external/groups`, `GET /api/v1/external/groups/{id}/messages`, `GET /api/v1/external/groups/{id}/participants` (novo scope `group.read.external`, independente de `group.read` do Workspace e nunca `group.manage`): leitura das mensagens/participantes de grupos de WhatsApp **habilitados** (opt-in por administrador, ADR-0015), narrowed pela allowlist de canais da instalação. Grupo continua **fora** de `contacts`/`conversations`/`messages` — modelo próprio (`wa_groups`/`wa_group_messages`), nunca misturado ao 1:1.
- Minimização preservada: `author_name` em toda mensagem/participante, **nunca** `author_jid`/telefone — mesma regra que ADR-0015 já impõe ao Workspace. Grupo desabilitado ou de outro tenant responde o mesmo `404` de um id inexistente (sem oráculo de enumeração).
- Lista de participantes é derivada de quem de fato enviou mensagem armazenada (`channel_participants` via `wa_group_messages.sender_channel_participant_id`) — não existe roster completo, não há tabela de membros do grupo no OMNIRA.
- Preview da última mensagem na listagem de grupos usa a mesma técnica em lote (`DISTINCT ON`) do preview de conversas (ADR §3.3) — nunca um lateral por linha.
- Migration `000171` (aditiva; só adiciona o scope ao `CHECK` de `external_app_installations.scopes`, cardinalidade já tinha espaço; nenhuma tabela nova — `wa_groups`/`wa_group_messages` já existiam).
- Testes `TestReadAPIListGroupsIsEnabledOnlyAllowlistedAndTenantScoped` e `TestReadAPIGroupMessagesAndParticipantsAreNameOnlyAndNeverCrossTenants` (Postgres real): grupo habilitado vs. desabilitado, allowlist de canal, outro tenant nunca vê, 403 sem o scope, 404 uniforme para grupo desabilitado/de outro tenant/inexistente, e asserção de que `@lid`/`author_jid` nunca aparecem na resposta. Suíte completa de `internal/external/adapters` executada (180s, sem regressão) e `internal/platform/httpserver` (paridade de contrato). Contrato `omnira-external-v1.yaml` atualizado (tag `Groups`, 3 rotas, schemas `Group`/`GroupMessage`/`GroupParticipant`/`GroupMessagePage`, scope no enum) e sincronizado em `docs/ops/crm-handoff/`. UI de Aplicações externas ganhou o checkbox do novo scope.

### Added (API externa — nota interna como mensagem private, ADR-0053, pendência CRM §3.4 — Opção B, paridade Chatwoot)
- `POST /api/v1/external/conversations/{id}/notes` (novo scope `message.note`, independente de `message.send`): registra uma nota interna, nunca enviada ao cliente, intercalada no mesmo feed de `GET .../messages` (`private: true`, `status: "noted"`). `201 Created`, nunca `202` — nada fica pendente de entrega. Mesma disciplina de posse/`If-Match`/`Idempotency-Key` do envio de mensagem real.
- **A barreira contra vazamento é estrutural, não um `if`**: o INSERT que grava a nota (`internal/messages/adapters/outbound_note.go`) não tem — e nunca deve ter — a cláusula que grava o job de entrega em `outbox_events`; é a ausência dessa cláusula, não uma checagem de flag, que garante que uma nota jamais alcança o WAHA ou a Meta Cloud API. Caminho de escrita (`ExternalNoteWriter`) e de store (`ExternalNoteStore`) deliberadamente separados do envio real (`ExternalSender`), para que um bug em um nunca afete o outro.
- Migration `000170`: coluna `messages.private`, novo valor de status `noted` (amarrado a `private` por `CHECK`), `message.note` no catálogo de scopes, `note` no catálogo de operações de `external_command_receipts`.
- Teste `TestWriteNoteIsNeverQueuedForDeliveryAndNeverCrossesTenants` (Postgres real): prova o invariante (zero linhas em `outbox_events` para a nota, inclusive após replay por idempotência), posse/If-Match, validação (texto vazio ou maior que 4000), 403 sem o scope, 404 para outro tenant, e que a nota aparece intercalada no feed. Suíte completa de `internal/external/adapters` executada (213s, sem regressão). Contrato `omnira-external-v1.yaml` atualizado (`POST .../notes`, schema `NoteRequest`, `private`/`noted` em `Message`, enum de scopes) e sincronizado em `docs/ops/crm-handoff/`. UI de Aplicações externas ganhou o checkbox do novo scope.
- **Revisão independente via Codex CLI** (`codex review --uncommitted`), conforme exigido pelo próprio ADR-0053 antes de considerar a fatia pronta, achou dois problemas reais fora do caminho externo: a nota (outbound, não-falha) estava sendo tratada como resposta real pelo cálculo de "cliente esperando" (Workspace e projeção do Hub) e aparecia sem rótulo no feed de mensagens do Workspace/Hub, na busca de histórico do contato e no Contact 360 — risco de um operador humano achar que o texto foi enviado ao cliente. Corrigido em `internal/inbox/adapters/http.go`, `internal/worker/hubprojector/projector.go`, `internal/hub/adapters/http.go`, `internal/attendance/adapters/postgres.go` e `internal/contacts/adapters/contact360.go`: uma nota privada agora é **inteiramente invisível** fora da API externa (excluída de toda contagem de "não respondido", de todo preview/feed e da busca), até que uma extensão de UI do Workspace decida exibi-la rotulada. Suítes de `internal/inbox/adapters`, `internal/worker/hubprojector`, `internal/hub/adapters`, `internal/attendance/adapters` e `internal/contacts/adapters` executadas contra Postgres real, sem regressão (um teste de SSE em tempo real do Hub, não relacionado, já falhava antes desta mudança — confirmado por comparação A/B).
- Migration: `000170` (aditiva; `down` remove notas existentes, o scope e o valor de status — rollback destrutivo para dados só internos, aceitável).

### Added (API externa — listar templates aprovados, ADR-0052, pendência CRM §2.2)
- Novo scope `template.read` (migration `000169`, catálogo fechado em `internal/external/domain/scope.go`) e `GET /api/v1/external/templates`: templates já aprovados pela Meta e sincronizados (`channel_message_templates`), narrowed pela allowlist de canais da instalação. `id` é o mesmo valor que `message.template_id` espera no envio — fecha a lacuna que hoje obriga o dono a passar UUIDs de template manualmente ao CRM.
- Sem escrita: sincronizar/criar/editar template continua exclusivo de `channel.manage` no Workspace.
- Teste `TestReadAPIListTemplatesIsApprovedOnlyAllowlistedAndTenantScoped` (Tenant A/B, Postgres real): só aprovado+sendable aparece, allowlist de canal filtra, outro tenant nunca vê, 403 sem o scope. Contrato `omnira-external-v1.yaml` atualizado (rota, schema `Template`, enum de scopes) e sincronizado em `docs/ops/crm-handoff/`. UI de Aplicações externas ganhou o checkbox do novo scope.
- Migration `000169` (aditiva; `down` remove o scope das instalações que o tiverem, suspendendo quem ficar sem nenhum scope restante).

### Added (API externa — busca de contato por telefone, pendência CRM §2.3)
- `GET /api/v1/external/contacts?phone_e164=+55...`: acha um contato pelo telefone e devolve `open_conversation_id` quando há conversa aberta visível a essa instalação. Mesma regra de visibilidade do `GET /contacts/{id}` já existente (precisa de uma conversa visível, nunca contato interno/spam) — não é porta nova, reusa o scope `contact.read`, sem scope novo. Não cria contato nem conversa (isso é ADR-0050, bloqueado por decisão do dono).
- Teste `TestReadAPISearchContactsByPhoneRespectsVisibilityAndNeverCrossesTenants` (Tenant A/B, Postgres real): contato com conversa aberta, contato só com conversa fechada (sem `open_conversation_id`), contato interno/de outro tenant/inexistente respondem o mesmo 404, validação de E.164, 403 sem o scope. Contrato `omnira-external-v1.yaml` atualizado (`GET /contacts`, campo `open_conversation_id`) e sincronizado em `docs/ops/crm-handoff/`. Sem migration.

### Added (API externa — preview da última mensagem na listagem, pendência CRM §3.3)
- `GET /api/v1/external/conversations` e `GET /api/v1/external/conversations/{id}` passam a trazer `last_message: { id, direction, type, body, created_at }` (nunca mídia nem remetente — isso continua em `GET .../messages`).
- Implementação: uma única consulta em lote (`DISTINCT ON`) por página, não uma subconsulta por conversa — uma primeira versão com `LEFT JOIN LATERAL` por linha media p95 > 2s sob carga real (`TestReadAPIListLatencyOnAMidSizeCompany`, 4000 conversas); a versão em lote mantém o mesmo teste estável (~20s de teste, sem violar o limite) e foi confirmada 3x consecutivas contra Postgres real.
- Teste novo: `TestReadAPIListIncludesLastMessagePreview` (preview correto da mensagem mais nova, conversa sem mensagem não tem `last_message`, corpo de outro tenant nunca aparece). Contrato `omnira-external-v1.yaml` atualizado (schema `LastMessagePreview`) e sincronizado em `docs/ops/crm-handoff/`. Sem migration.

### Added (Aplicações externas — editar sem recriar, ADR-0048, pendência CRM §3.6)
- Tela "Aplicações externas" ganhou botão **Editar** (nome, escopos, filas/linhas da allowlist), reaproveitando o `PATCH /tenants/{t}/integrations/apps/{app_id}` que já existia no backend (testado) mas nunca era chamado pela tela — até agora, qualquer ajuste exigia criar outra aplicação. `rate_limit_per_min` e `lease_seconds` continuam fora da tela (decisão já tomada no commit `9df1010`).
- **Histórico** por aplicação: `GET /tenants/{t}/integrations/apps/{app_id}/audit` (novo `AdminHandler.History`, novo `AuditEventRepository.FindByResource`), listado na tela — quem criou/alterou/suspendeu/revogou e quando, sem token nem segredo.
- Testes: `TestEditingNameScopesAndAllowlistIsAuditedAndNeverCrossesTenants` (Tenant A/B, Postgres real) — editar não cruza tenant, histórico de A é 404 para B em toda rota (leitura, escrita, auditoria), supervisor lê o histórico mas não edita. Frontend: 2 testes novos em `ExternalAppsPage.test.tsx` (editar e mostrar histórico). Sem migration.
- **Não implantado ainda** (só no branch/working tree; falta build e deploy das imagens `api`/`web`).

### Added (Multi-Tenant Service Hub — estado `LAB`, migrations 093–121)
- Hub como plano de controle: contratos, concessões, painel `/acessos`, convites por e-mail, pools e distribuição, auditoria; atender uma empresa pelo Hub (`X-Omnira-Acting-As`) com chaves delegáveis; aba "Todas" no layout aprovado (fila × histórico, abas, painel recolhível) com sinal de mudança em tempo real (`GET /api/v1/hubs/{id}/events`, SSE sem conteúdo).
- Isolamento: RLS FORCE, contexto de sessão assinado em `enforce` (ADR-0042, migration 118), escopo das escritas de auditoria/outbox (121), checagem de mesma origem nas escritas por cookie, limpeza de cache/rascunhos ao sair.
- Escala/operação: `hub_reach()` (migration 119), métricas `omnira_hub_*`, `scripts/hub-health-check.sh`, guias em `docs/ops/HUB-OPERATOR-GUIDE.md` e `docs/architecture/HUB-{DATA-MODEL,MIGRATION-GUIDE,GATE-PACKAGE}.md`. Gate formal **não assinado**.

### Fixed (Flow Builder — chamado sem assunto)
- `find_open_tickets` passa a nomear o chamado pelo assunto, ou por `nº <número do CRM/ERP>` quando o assunto local está vazio (chamado aberto no CRM pelo Inbox), ou por `sem assunto registrado`. Antes o cliente lia `chamado aberto: ""` e o resumo do atendente terminava em `existe: .` (smoke com telefone real, 2026-10-07).
- **Implantado em 2026-10-08** (só o `worker`, que executa os fluxos; sem migration). Rollback: `20-omnira-worker:rollback-pre-ticketlabel-20261007-2054`.
- O fluxo publicado "Recepção inteligente" (versão 2 do tenant, instalada antes do aviso ao cliente entrar no modelo) não avisa o cliente ao passar para um atendente. Um **rascunho NÃO publicado** (revisão 3) com os avisos de transferência (chamado existente, suporte, financeiro, comercial, outro assunto) foi salvo; a versão em uso não mudou. Falta o dono simular e publicar.

### Added (aviso de abertura de chamado ao cliente — ligado por padrão)
- Quando o operador abre um chamado no CRM/ERP do tenant (`POST .../conversations/{id}/ticket`), o cliente recebe na própria conversa a mensagem "Prezado {nome}, Sua solicitação foi recebida com sucesso… Protocolo do chamado: {número}… Equipe de Suporte". O número é o `external_ticket_id` devolvido pelo CRM/ERP; o nome é o do contato (sem nome utilizável, "Prezado cliente"). Texto em `internal/messages/application/ticket_notice.go`.
- É um texto de saída comum enfileirado pelo `Sender` (mesmas permissões, janela de 24 h e entrega de qualquer resposta, atribuído ao operador que abriu o chamado). A chave de idempotência é o chamado local: repetir ou reexecutar a criação nunca manda duas vezes, e um replay dá nova chance a um aviso que falhou. É **best-effort** dentro de um savepoint: janela fechada, canal fora ou conversa sem número só registram no log e **nunca** mudam a resposta do chamado. Só dispara para chamado realmente criado/replay confirmado (não para rejeição, vínculo existente ou desfecho incerto).
- `OMNIRA_TICKET_OPEN_NOTICE_ENABLED=false` desliga (padrão: ligado). Sem migration. Fluxos (Flow Builder) abrem chamado **local** e não passam por aqui.
- **Implantado em 2026-10-08, LIGADO** (só a `api`, sem migration; backup verificado `omnira_dev_20261008T003922Z`, copia externa e na nuvem). Rollback: imagem `20-omnira-api:rollback-pre-ticketnotice-20261007-2039`. Verificado: api saudável, texto presente no binário, rota do chamado e webhook da Meta respondem 401 sem credencial. A flag não está no `docker-compose.yml` da árvore principal (outra branch); sem ela o padrão é ligado, e para desligar é preciso passar `OMNIRA_TICKET_OPEN_NOTICE_ENABLED=false` ao contêiner.

### Added (R-4 — anexos de saída, ADR-0024; desligado por flag)
- O operador anexa imagem, áudio, vídeo MP4, PDF ou texto à resposta (clipe no Inbox). Upload `POST .../attachments` (tipo pelo conteúdo, metadados de imagem removidos, ClamAV que **fecha** se estiver fora, nome saneado, 16 MiB, 5 pendentes/conversa, 24 h) e envio com `attachment_id` (legenda ≤ 1024). Entrega pelo WAHA (`sendImage/Video/Voice/File`) e pela Meta (upload de mídia + mensagem); falha ambígua termina `uncertain` e nunca é reenviada. Migration `000092`, `OMNIRA_OUTBOUND_MEDIA_ENABLED`, `docs/ops/OUTBOUND-MEDIA.md`. Falta o teste ao vivo com número de teste.
- **Implantado em 2026-10-07 com a flag DESLIGADA** (api, worker e web recriados; migration `000092` aplicada depois de backup verificado; `OMNIRA_OUTBOUND_MEDIA_HOST_PATH=/opt/omnira-media/outbound`). Rollback: imagens `20-omnira-{api,worker,web}:rollback-pre-outboundmedia-20261007-1156`. Verificado ao vivo: a API grava só em `outbound/` (`clean/` e `quarantine/` continuam somente leitura), o worker grava em `outbound/`, o ClamAV real bloqueia o EICAR e a API o alcança, o login por cookie do Web segue íntegro.

### Added (ADR-0018 adendo 2026-10-12 — quem vê conversas de contatos "Internos")
- Permissão **`conversation.read_internal`** (migration `000120`, aditiva): administradores e supervisores; agentes e agentes do Hub não. Sem ela, as conversas de contatos internos somem das listas e toda rota que as nomeia (conversa, mensagens, mídia, chamado, histórico do contato) responde 404 — decisão única no middleware de autorização. A tela só oferece "Internas" a quem tem a chave.
- Conversas de contatos internos não entram na fila automática (como spam) e saem da fila ao declarar o contato interno; o projetor do Hub as ignora.
- Limites registrados no ADR-0018 (conversa já atribuída, SSE só com identificadores, contadores agregados, conversas com equipe verificada fora desta chave).

### Added (ADR-0018 adendo — contato "Interno": equipe, parceiro, fornecedor)
- Nova classificação de pessoa **Interno** (ao lado de Cliente e Outros) com subtipo **Equipe / Parceiro / Fornecedor**. É declaração de um operador, não identidade verificada: só desliga a automação de cliente (bot, chamado automático, SLA, CSAT); a conversa fica atribuível à mão (`conversation_kind` continua `external_other`), sem fila automática. Migration `000090` (aditiva, reversível; o `down` devolve esses contatos a "Outros").
- Só decisão humana (`source=manual`, permissão `contact.classify`), papel obrigatório (422/400), auditoria com `internal_role_from/to`; a tela diz o que a escolha desliga antes de salvar. `PUT …/kind` recusa `internal`.
- Inbox: o filtro **Internas** inclui esses contatos (e `kind=internal`); a lista mostra o selo do subtipo; a visão "Internos" do diretório de pessoas traz Users (com `membership.read`) mais contatos internos. Contrato `omnira-v1.yaml` atualizado. Detalhes e adiados (vincular a User, sugestão por telefone): ADR-0018, adendo de 2026-10-07.

### Added (MOBILE.1 — credenciais de apps nativos, ADR-0022; desligado por flag)
- Migration `000091`: `auth_devices`, `auth_families`, `auth_refresh_tokens`, `auth_access_tokens` (só *digests* SHA-256). `POST /auth/mobile/{token,refresh,logout}`, `GET /me/devices`, `DELETE /me/devices/{device_id}`; token de acesso opaco de 15 min, *refresh* de uso único (30 d deslizante, 90 d absoluto), reuso revoga o aparelho, trava de família serializa refresh × revogação. `OMNIRA_AUTH_MOBILE_ENABLED/CLIENT_ID/REDIRECT_URIS`. Contrato em `docs/auth/MOBILE-AUTH.md`.
- **Implantado em 2026-10-07** (só a `api`; migration `000091` aplicada depois de backup verificado; `OMNIRA_AUTH_MOBILE_ENABLED` continua **desligada** em produção até existir um app). Rollback da imagem: `20-omnira-api:rollback-pre-mobilecore-20261007-1018` (a migration é aditiva e o `down` a remove). Validado ao vivo contra o Keycloak real (cliente `omnira-mobile` criado: público, PKCE S256 obrigatório, sem direct grants): login com Code+PKCE, troca de código, refresh, reuso, logout, SSE fechando na revogação, e regressão do login por cookie do Web.
- Administração de aparelhos de outro usuário (`GET/DELETE /tenants/{tenant_id}/team/{membership_id}/devices[/{device_id}]`, só com a flag): `membership.manage` em todos os tenants do alvo, auditado.
- R-3: o SSE reverifica a sessão (cookie ou aparelho), não só a membership.
- R-2: cotas por tenant e por usuário aplicadas **depois** da membership verificada (`OMNIRA_RATELIMIT_USER_PER_MIN=1200`, `OMNIRA_RATELIMIT_TENANT_PER_MIN=6000`).

### Added (MOBILE.READINESS — preparar o Core para clientes Android/iOS; nenhum app é construído)
- `X-Request-ID` em toda resposta da API e **envelope de erro estável opt-in** (`Accept: application/vnd.omnira.v1+json` → `{"error":{"code","message","request_id"}}`); sem esse cabeçalho nada muda para o Web. Documentado em `omnira-v1.yaml`.
- Eventos em tempo real (SSE/NATS) com `event_id` e `v` (aditivos; também o campo SSE `id:`); `contracts/asyncapi/omnira-v1.yaml` atualizado. O Web ignora o `id:` (teste do parser real).
- Teste de superfície de segurança: toda operação documentada sob `/tenants/…` e `/me` (omnira, attendance, flows) recusa chamadas sem credencial, Bearer inválido e tenant/usuário "forjados" por cabeçalho (401, sem pânico).
- Documentação: `docs/architecture/mobile-readiness.md` (mapa, matriz de capacidades, achados P0–P3, roteiro MOBILE.0–16), ADR-0021 (arquitetura do cliente, segurança do app, offline), ADR-0022 (autenticação Web × Mobile — **aceita** pelo dono em 2026-10-07, valores propostos; implementação no MOBILE.1), ADR-0023 (eventos e notificações). Sem migration, sem dependência, sem rota nova, sem flag ligada.
- **Implantado em 2026-10-07** (api e worker recriados; web inalterado; sem migration). Rollback: imagens `20-omnira-api` e `20-omnira-worker` com a tag `rollback-pre-mobilereadiness-20261007-0936`. Revisão independente do Codex (2 passadas) registrada em `docs/architecture/mobile-readiness.md`.

### Added (IA da plataforma com Gemini)
- `OMNIRA_AI_PROVIDER=gemini`: `GeminiGenerator` na porta `TextGenerator` (chave só no cabeçalho, sem redirecionamento nem retentativa, resposta limitada, bloqueio do provedor tratado). `NewTextGenerator` passa a ser o único construtor (resumo, roteador de modelos e nós de IA dos fluxos). `OMNIRA_GEMINI_API_KEY` vale como chave quando o provedor é Gemini. Guia: `docs/ai/PLATFORM-AI-PROVIDERS.md`.

### Fixed (webhook da Meta com 502 após recriar a api)
- `web/nginx.conf` resolvia `api:8080` só no início do nginx; recriar a `api` (IP novo) deixava o webhook oficial da Meta respondendo 502 (mensagem não chegava ao Inbox). Agora usa `resolver 127.0.0.11 valid=5s` e `proxy_pass` por variável. Teste de regressão `scripts/test-web-nginx-resolver.sh` (falha com a configuração antiga).

### Fixed (IA da plataforma)
- `docker-compose.yml` não repassava `OMNIRA_AI_*`, `OMNIRA_COPILOT_ENABLED`, `OMNIRA_AI_TOOL_GATEWAY_ENABLED` nem os demais interruptores do Conversation Intelligence aos contêineres: nenhum valor do `.env` chegava à api/worker. Agora são repassados (vazio = padrão do código).

### Added (ADR-0020 — Finalizar atendimento e memória do contato; implementado em `main`, NÃO implantado)
- **Finalizar atendimento** (Inbox): fecha o episódio de atendimento em uma transação (mesma trava do motor de fluxos, idempotente), libera o bot, encerra só chamados locais (ERP/assunto ficam), registra motivo, resumo e o que ficou pendente/prometido/para lembrar, audita ids e contagens. Migration `000086` (aditiva, reversível): `conversation_closures` (imutável) e `follow_up_items`, RLS FORCE. Sem permissão nova (`conversation.claim`/`manage`).
- API `contracts/openapi/attendance-v1.yaml` (6 operações, com teste de deriva): finalizar, sugerir com IA, contexto da conversa, busca no histórico do contato, histórico do contato, resolver pendência.
- Interface: diálogo "Finalizar atendimento", painéis "Pendências do contato" e "Atendimentos anteriores", busca no histórico, aba "Encerradas", conversa finalizada sem campo de resposta. A lista do Inbox passa a mostrar só atendimentos abertos por padrão (`status=open|closed|all`) e o envio a conversa finalizada é recusado (409).
- Copiloto (desligado por padrão): memória do mesmo contato no contexto (`OMNIRA_COPILOT_CONTACT_MEMORY_ENABLED`, prompt v2), ferramentas de leitura `contact.recent_attendances`, `contact.open_followups` e `contact.search_history` no gateway, e "Sugerir com IA" ao finalizar (rascunho `ai_inferred`, só leitura, sem trava). Detalhes e adiados: `docs/attendance/README.md`.

### Added (ADR-0019 — Visual Flow Builder, LAB, desligado por padrão)
- Fluxos de atendimento configuráveis: rascunho editável, versões **imutáveis** (trigger no banco) e ponteiro ativo com rollback; 6 tabelas novas com RLS `FORCE` (migrations 000082–000084, aditivas e reversíveis).
- Motor de execução transacional por conversa (idempotente, com retomada, limites, subflows com versão fixada), 20 tipos de nó, simulador sem efeitos, nós de IA opcionais que só sugerem.
- Biblioteca de 24 templates em 3 packs (OMNIRA Starter, K3G Support, ISP NOC), instalados como rascunhos do tenant (atômico, só filas do próprio tenant).
- API `contracts/openapi/flows-v1.yaml` (23 operações) com permissões `flow.*` (editar e publicar separados), histórico de execuções, analytics medidos e métricas Prometheus.
- Envio do bot por `SystemSender` (mesma janela de 24 h e mesmo job de entrega; calado quando há operador).
- Integração opcional ao ingest (`FlowGate`), consumidor `worker-flows` e sweeper; flags `OMNIRA_FLOWS_ENABLED` e `OMNIRA_FLOWS_AI_ENABLED` (padrão `false`).
- Frontend: editor visual, biblioteca/assistente de instalação e execuções (rotas `/flows`, `/flows/:flowId`).
- Docs: `docs/flows/{STATUS,PRODUCTION-GATE,FINAL-REPORT,USER-GUIDE,OPERATIONS,CONFLICT-ANALYSIS}.md`.

### Fixed
- Registry-size test of the AI tool gateway updated 5 -> 8 (the three new tools are read-only; a test now pins that).
- Web: the app now runs on a data router (`createBrowserRouter`); the flow editor asks before any navigation, including browser Back/Forward, drops unsaved edits (FLOW-302), and after saving a blank or padded name the field matches what the server stored (FLOW-501).
- Flow Builder (Codex rounds A/B): the editor no longer discards unsaved edits when settings are saved or a newer server revision arrives (explicit conflict + reload), asks before in-app navigation drops them; the ingest returns a bot-held new conversation to normal routing right away when its flow job cannot be enqueued.
- Flow Builder (Codex review): `000085` makes a flow's active version belong to that same flow (composite FK); question nodes route a closed 24h window; the inbound gate no longer enqueues jobs the engine would discard.
- `go build ./...` em `main`: import sem uso em `authn/password_handler.go` e `HandleFunc` recebendo `http.Handler` em `httpserver/server.go:214` (IAM5).

### Added (M03.1 — Inbound persistence foundation)
- Provider-neutral inbound orchestration for Contact, Conversation, Message and Ticket.
- PostgreSQL adapter scoped by TenantContext and transaction querier.
- Provider-message idempotency: a redelivery cannot create a second message or ticket.
- WAHA webhook intake now reserves dedupe and persists the canonical message in one transaction.
- System TenantContext derives ownership from the persisted ChannelConnection without a fabricated user UUID.
- Provider-message uniqueness is scoped by ChannelConnection, with concurrent open-conversation/active-ticket guards.
- Inbound media metadata is persisted canonically without retaining the WAHA payload.
- `message.ack` updates outbound delivery status within the trusted connection scope and cannot regress delivered/read state.
- Atomic manual conversation claim with exactly one concurrent winner and append-only assignment history.
- Routing claim derives both Tenant and target operator from TenantContext; cross-tenant attempts remain indistinguishable from conflicts.
- Atomic system round-robin selects only active, available queue members below capacity and records system-owned assignment history.
- Round-robin derives Tenant from trusted system context; asynchronous Outbox/NATS dispatch remains a separate slice.
- PostgreSQL Outbox now honors the transaction querier, preserving atomic domain-write/event-write rollback semantics.
- Routing jobs use the canonical `job.routing.assign.v1` JetStream subject rather than the legacy event subject formatter.
- Durable routing consumer resolves tenant ownership from the persisted conversation and ignores envelope `tenant_id` for authorization.
- Round-robin redelivery is idempotent: an assigned conversation returns its existing owner without duplicating assignment history.
- Each tenant may mark one explicit default queue; new inbound conversations enter it without name-based or payload-based inference.
- Initial round-robin routing job is written atomically with inbound persistence; manual default queues remain available for operator claim.
- Outbox dispatcher now executes read, attempt and publish-mark operations inside transaction-local system sessions, compatible with `omnira_app` and RLS.
- Worker no longer emits database or NATS connection URLs to logs.
- Outbox UPDATE policy is restricted to the system worker session (`000021`); fresh migration bootstrap is now valid.
- D3.1 PostgreSQL adapter tests use the canonical TenantContext and compile with the existing `lib/pq` implementation.

Todas as mudanças relevantes do OMNIRA serão registradas aqui.

## [Unreleased]

### Added (M02 — Contact foundation)
- Tenant-owned provider-neutral `Contact` domain with canonical E.164 phone identity.
- PostgreSQL `contacts` migration with RLS, FORCE RLS, tenant-scoped policies, unique tenant/phone identity, and rollback.
- TenantContext-guarded repository and idempotent `UpsertByPhone` identity-resolution service.
- Contact state transitions for active, blocked, and archived records.

### Added (M03 — Conversation, Message and Ticket foundation)
- Tenant-owned conversation/message/ticket schema with composite tenant FKs and RLS/FORCE RLS.
- Canonical lifecycle models for open/closed conversations, inbound/outbound messages, and ticket resolution.
- Queue, routing, realtime inbox, and automation remain outside this slice.

### Added (M04 — Queue and routing foundation)
- Tenant-owned queues, queue members, availability/capacity and append-only assignment events.
- Manual and round-robin modes with deterministic oldest-assignment selection.
- Composite tenant FKs and RLS/FORCE RLS for routing data.
- Atomic claim remains a SQL application operation; no read-then-write claim path introduced.

### Added (U4 — WAHA outbound text foundation)
- WAHA `SendText` adapter using the canonical channel seam and `/api/sendText`.
- E.164 recipient validation, deterministic connection session ownership, and canonical sent result.
- Provider error classes for authentication, rate limiting, unavailable provider, disconnected session, configuration, permanent, transient, and unknown failures.
- Low-cardinality OTel `channel_operation_total` and `channel_operation_error_total` metrics.
- Idempotency remains owned by the Outbox/application layer; no secret or arbitrary provider payload crosses the domain seam.
- Worker delivery boundary consumes only `connection_id` plus outbound message references; tenant scope is reconstructed by `TenantSessionRunner`.
- Retry policy does not retry authentication, invalid configuration, permanent validation, or disconnected sessions.
- WAHA `message.ack` is normalized to canonical delivery states, and inbound media download is restricted to the configured WAHA origin with a 25 MiB limit.

### Added (U3 — WAHA webhook and inbound foundation)
- Connection-scoped webhook route: `POST /webhooks/v1/whatsapp/waha/{connection_token}`.
- Raw-body HMAC-SHA512 verification with constant-time comparison and per-connection CredentialStore secret.
- Session webhook configuration limited to `message.any`, `message.ack`, and `session.status`.
- WAHA message normalization to canonical `InboundMessage`; provider echoes ignored.
- System transaction resolves connection ownership; webhook payload never supplies tenant authority.
- Body limit and OTel webhook counters with low-cardinality labels.

### Added (U2 — WAHA session management)
- Deterministic `omnira_<connection-id>` session reference; arbitrary names rejected.
- Create/start/stop/restart/get session, QR, account identity, and provider health operations.
- WAHA statuses normalized to OMNIRA states; unknown states degrade safely.
- Persisted `provider_session_ref` with migration rollback.
- Tenant/connection identity checks in adapter; no tenant ID accepted from WAHA.

### Added (U1 — WAHA unofficial runtime foundation)
- Optional internal-only WAHA Docker service pinned to `devlikeapro/waha:gows-2026.8.2`.
- Persistent session/media volumes, healthcheck, restart policy, disabled dashboard/Swagger.
- Centralized WAHA config and engine validation (`GOWS`, `NOWEB`, `WEBJS`).
- Provider-agnostic WAHA HTTP client foundation with API-key transport and error classification.
- No WAHA port published to host; API key remains runtime secret.

### Added (D3.1 — Channel credentials foundation)
- `channel_connections` e `channel_credentials` com RLS + FORCE RLS e rollback.
- AES-256-GCM com chave Base64 `OMNIRA_CREDENTIALS_KEY` de 32 bytes.
- `CredentialStore` e repositórios PostgreSQL tenant-aware.
- Testes de isolamento A/B, ciphertext, chave errada e resolução de credencial.
- Contadores OTel de resolução de credenciais sem IDs ou segredos.

### Added (D3.2 — Meta webhook verification foundation)
- Verificação de challenge Meta e assinatura HMAC-SHA256 sobre raw body.
- Handler limitado a 1 MiB, resolução por `phone_number_id` confiável e bloqueio de conexão inativa/não oficial.
- Testes sem dependência de chamada externa à Meta.

### Added (T01–T02 — Bootstrap + Local Infrastructure)

#### T01 — Bootstrap Go repo
- Estrutura Go modular: `apps/api`, `apps/worker`, `internal/` com domain/application/ports/adapters.
- `go.mod` com dependências essenciais: pgx, NATS, OpenTelemetry, UUID.
- `config.Load()` com 12-factor env vars: OMNIRA_ENV, OMNIRA_HTTP_ADDR, OMNIRA_DATABASE_URL, OMNIRA_NATS_URL, etc.
- `httpserver.Server`: graceful shutdown, `/internal/health/{live,ready,modules}` endpoints.
- `cmd/omnira-api/main.go`: inicia servidor HTTP, responde health checks.
- `cmd/omnira-worker/main.go`: bootstrap worker com graceful shutdown.
- `Makefile`: targets build, test, fmt, vet, run-api, run-worker, docker-up/down.
- Testes unitários: `config_test.go`, `main_test.go` para health endpoints e graceful shutdown.

#### T02 — Local infrastructure (docker-compose)
- `docker-compose.yml`: Postgres 16, NATS 2.10 com JetStream, OTel Collector (padrão).
- `config/otel-collector-config.yml`: OTLP receivers (gRPC/HTTP), debug exporter, health check extension.
- `tools/dev-up.sh`: sobe infrastructure, aguarda health checks, exibe endpoints.
- `tools/dev-down.sh`: derruba infrastructure.
- `tools/check-docker-health.sh`: valida saúde de Postgres, NATS, OTel (script determinístico reutilizável em CI).
- `docs/development/LOCAL-SETUP.md`: guia de setup local, troubleshooting, volume management.
- Volumes persistentes: `postgres_data`, `nats_data`.
- Health checks: todos os serviços validam saúde com retry.

### Changed
- `docs/architecture/ARCHITECTURE.md`: corrigido para refletir Go exclusivo (era FastAPI/NestJS).
- `AGENTS.md`: adicionadas seções "Automation First" e "Automação como Prioridade" com hierarquia tool/skill/agent.
- `Makefile`: env vars corrigidas para porta 55434 (Postgres), localhost:4317 (OTel GRPC).

### Tested
- `go test ./...` passa (config, httpserver).
- `go build ./apps/api/cmd/omnira-api` / `omnira-worker` compilam.
- Health endpoints: `/internal/health/{live,ready,modules}` → 200 OK.
- `tools/dev-up.sh`: sobe ambiente em ~10s, todos os serviços healthy.
- `tools/check-docker-health.sh`: exit code 0 (todos os checks passam).

### Automation
- **Script criado:** `tools/check-docker-health.sh` (determinístico, reutilizável em CI para validar infra).
- Regra dos 2 usos: será promovido para CI após T03 (quando usado na validação de migrations).

### Docker-First (Decisão congelada)
- `docs/deployment/DOCKER-FIRST.md`: especificação completa de containerização obrigatória.
- `Dockerfile.api`: multi-stage build para omnira-api (Go), usuário não-root, healthcheck, metadata.
- `Dockerfile.worker`: multi-stage build para omnira-worker (Go), usuário não-root.
- `internal/platform/config`: adicionadas vars globais Version, Commit, BuildTime para injeção via ldflags.
- `docs/delivery/DEFINITION-OF-DONE.md`: adicionada seção Docker-First com requisitos obrigatórios.
- `AGENTS.md` + `START-HERE.md`: Docker-first integrado como princípio congelado.
- Imagens testadas: omnira-api:0.1.0 (8.77MB), omnira-worker:0.1.0 (5.77MB).

### Added (T03 — Migration framework + schema base)
- `migrations/000001_initial_schema.up/down.sql`: users, tenants tables com RLS enabled.
- `migrations/000002_memberships_rbac.up/down.sql`: memberships, roles, permissions, role_permissions com dados seed.
- `migrations/000003_audit_outbox.up/down.sql`: audit_events, outbox_events com índices de performance.
- `migrations/000004_rls_policies.up/down.sql`: RLS policies (tenant boundary) + helper functions (current_user_id, is_system_admin).
- `tools/apply-migrations.sh`: wrapper pra aplicar/validar migrations.
- `tools/validate-schema.sh`: valida presença de tabelas, RLS enabled, seed data (9 permissões, 5 roles).
- `tools/test-isolation.sh`: smoke test de isolamento tenant (A vs B).
- `Makefile`: targets migrate-up, migrate-down, migrate-validate, test-isolation.

### Added (Local AI — camada auxiliar opcional, fora do escopo do produto)
- `tools/ai/lib.sh`: client único para llama.cpp (ai_json, ai_sanitize, ai_metrics) com fallback e timeout.
- `tools/ai/triage-logs.sh`: reduz logs extensos a diagnóstico JSON curto; preserva log bruto.
- `tools/ai/triage-tests.sh`: resume falhas de teste; veredito pass/fail permanece determinístico (grep).
- `docs/architecture/LOCAL-AI.md`: escopo, limite de autoridade, rede, privacidade, fallback.
- `.env.example`: config da aplicação + `LOCAL_AI_*` (nenhum valor real).
- `.gitignore`: `.env`, chaves e métricas locais fora do Git.
- `AGENTS.md`: seção "Local AI — uso permitido" com a proibição de usá-lo como evidência final.
- Detectado (sem alterar): `llama-server` em 18088 (hermes-3-llama-3.1-8b, GPU) e 18090 (Devstral, CPU). Default 18088 por latência (~0,25 s vs ~40 s).

### Added (T05 — Tenant + Membership domain)
- `internal/tenancy/domain/tenant.go`: `Tenant` aggregate (LegalName, IsolationProfile, Status).
- `internal/tenancy/domain/membership.go`: `Membership` aggregate (TenantID, UserID, RoleID, Status).
- `internal/tenancy/domain/role.go`: `Role` (system roles + tenant-scoped).
- `internal/tenancy/ports/repository.go`: `TenantRepository`, `MembershipRepository`, `RoleRepository` interfaces.
- `internal/tenancy/application/service.go`: `TenantService`, `MembershipService` com lógica de negócio.
- Testes de invariantes: Tenant ativas/inativas/suspensas, Memberships ativas/revogadas.
- Factory functions (`NewTenant`, `NewMembership`) com validação de invariantes.

### Added (T04 — Authentication adapter)
- `internal/platform/authn/authn.go`: `Authenticator` interface + `JWTAuthenticator` (RSA, OIDC-compatible).
- `Principal` struct (UserID, Subject) — único ponto de entrada de identidade autenticada.
- `Middleware()` — HTTP middleware que valida Bearer token, injeta Principal no context.
- `WithPrincipal()` / `FromContext()` — armazenar/recuperar Principal de context.Context.
- Testes: JWT válido, expirado, issuer/audience inválidos, middleware sem token.
- `go.mod` → github.com/golang-jwt/jwt/v5 v5.3.1.

### Added (T13 — Backup/Restore validation drill)
- `tools/backup-restore-test.sh`: teste end-to-end de backup/restore com RPO/RTO medido.
- **5 fases**:
  1. Criar backup do banco (pg_dump → plain SQL).
  2. Registrar baseline (contagem de linhas por tabela).
  3. Simular perda de dados (DELETE audit_events, outbox_events).
  4. Restaurar do backup (psql → SQL restore).
  5. Validar integridade (contagens após restore = baseline).
- Métricas capturadas:
  - RPO (Recovery Point Objective): All data backed up.
  - RTO (Recovery Time Objective): ~102ms (restore time).
  - Backup time: ~106ms.
  - Backup size: 44K (para schema + seed data).
- Teste de sucesso: data integrity verified, baseline matches restored state.
- Exit code 0 (PASS), limpa backup files após sucesso.
- Uso: `OMNIRA_DATABASE_URL=... ./tools/backup-restore-test.sh`.

### Added (T12 — Isolation test harness)
- `internal/tenancy/adapters/isolation_test.go`: suite adversarial de testes de isolamento tenant.
- **4 cenários de teste contra PostgreSQL real**:
  - TestIsolation_TenantACannotReadTenantBData: Tenant A vs B data separation.
  - TestIsolation_MembershipRevocation_DeniesAccess: User revogado nega acesso mesmo com tenant ativo.
  - TestIsolation_MultiTenantDataSeparation: 3 tenants com 3 users cada, validar isolamento completo.
  - TestIsolation_TenantInactive_DeniesAccess: Tenant inativo nega acesso mesmo com membership ativa.
- Testes rodam contra OMNIRA_DATABASE_URL (PostgreSQL real com migrations).
- Validam: data separation, membership enforcement, tenant status checks.
- Determinísticos: cada teste cria seus próprios dados, sem dependências de estado anterior.
- Requer migrations aplicadas (tables, RLS enabled).

### Added (T11 — OpenTelemetry baseline)
- `internal/platform/otel/tracer.go`: InitOTel() inicializa tracer + meter providers com OTLP exporters (gRPC).
- Configura resource com ServiceName, ServiceVersion.
- Tracer batching, Meter periodic reader.
- Cleanup function para shutdown gracioso.
- `internal/platform/otel/middleware.go`: HTTPMiddleware com tracing automático.
- Correlation ID tracking: extrai X-Correlation-ID header, gera novo se ausente, injeta no response.
- Span attributes: method, url, target, correlation_id, status_code.
- GetCorrelationID(), WithCorrelationID() helpers para acessar correlation ID no context.
- `internal/platform/otel/middleware_test.go`: 5 testes (GeneratesCorrelationID, PreservesCorrelationID, GetCorrelationID_Missing, WithCorrelationID, StatusCode).
- Testes validam: geração de ID, preservação de ID existente, captura de status codes.
- Exporters OTLP (gRPC): conectam ao OTel Collector para exportar traces e métricas.
- Dependency added: go.opentelemetry.io/otel v1.46.0, exporters, gRPC stubs.

### Added (T10 — Outbox pattern + NATS publisher ready)
- `internal/outbox/domain/event.go`: OutboxEvent aggregate para padrão Outbox.
- EventType enum (tenant.created, membership.granted, etc), AggregateType enum.
- Correlation tracking (CorrelationID, CausationID) vinculado a operações.
- Payload JSONB para dados do evento.
- PublishedAt timestamp (null até publicado), Attempts (retry tracking).
- MarkPublished(), RecordAttempt(), IsPublished() métodos.
- `internal/outbox/domain/errors.go`: validação de tenant_id.
- `internal/outbox/ports/repository.go`: OutboxEventRepository interface.
- `internal/outbox/application/service.go`: OutboxService (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- `internal/outbox/application/service_test.go`: 5 testes (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- Transacional: eventos registrados no banco junto com operação original (atomicidade garantida via DB).
- Publisher worker (não implementado T10): consumir FindUnpublished(), enviar ao NATS JetStream, chamar MarkPublished().

### Added (T09 — Audit events + correlation IDs)
- `internal/audit/domain/event.go`: AuditEvent aggregate com correlation tracking.
- AuditAction enum (tenant.created, membership.granted, etc), AuditOutcome (success/failure).
- ResourceType enum (tenant, membership, role).
- CorrelationID para rastrear operações relacionadas, CausationID para causalidade.
- Metadata JSONB para contexto da ação (legal_name, profile, etc).
- `internal/audit/domain/errors.go`: validação de tenant_id, actor_id.
- `internal/audit/ports/repository.go`: AuditEventRepository interface.
- `internal/audit/application/service.go`: AuditService (RecordEvent, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- `internal/audit/adapters/postgres.go`: PostgresAuditEventRepository (Store, FindByID, FindByTenantAndCorrelation, FindByTenant, FindByAction).
- `internal/audit/adapters/http_handlers.go`: AuditAPIHandler (GET /api/v1/tenants/{tenant_id}/audit/events com paginação).
- `internal/audit/application/service_test.go`: 5 testes (RecordEvent, EventMetadata, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- RLS no nível de banco: audit_events.tenant_id protegida por RLS policies.

### Added (T08 — Tenant API HTTP endpoints)
- `internal/tenancy/adapters/http_handlers.go`: HTTP handlers para Tenant API.
- `TenantAPIHandler`: GetTenantMe, ListMemberships, CreateMembership, RevokeMembership.
- Response types: TenantResponse, MembershipResponse (JSON serializable).
- **Endpoints**:
  - `GET /api/v1/tenants/me` — retorna tenant do TenantContext (requer auth + authz).
  - `GET /api/v1/tenants/{tenant_id}/memberships` — lista memberships do tenant.
  - `POST /api/v1/tenants/{tenant_id}/memberships` — cria membership (user_id, role_id).
  - `DELETE /api/v1/tenants/{tenant_id}/memberships/{membership_id}` — revoga membership.
- Middleware chain: authn (Principal) → authz (TenantContext) → handler.
- `apps/api/routes.go`: RegisterRoutes() registra health endpoints + Tenant API routes.
- `internal/tenancy/adapters/http_handlers_test.go`: 5 testes (GetTenantMe, ListMemberships, CreateMembership, RevokeMembership_ServiceOnly, MissingTenantContext).
- Testes validam status codes (200/201/204/500/400), response JSON, e fluxo auth.

### Added (T07 — RLS / Persistence isolation)
- `internal/tenancy/adapters/postgres.go`: PostgreSQL repository implementations (Tenant, Membership).
- `NewPostgresTenantRepository()`, `NewPostgresMembershipRepository()` — adapters concretos.
- Store, FindByID, FindAll, Update — CRUD operations com queries parametrizadas.
- FindByTenantAndUser, FindByTenant — queries tenant-scoped.
- `internal/tenancy/adapters/postgres_test.go`: 5 testes contra PostgreSQL real (com migrations aplicadas).
- **Testes**:
  - TestPostgresTenantRepository_Store — Store + FindByID validam persistência.
  - TestPostgresMembershipRepository_Store — Store + FK constraints validados.
  - TestRLSIsolation_TenantACannotReadTenantB — isolamento de tenant (placeholder para RLS application-level).
  - TestMembershipRevoked_NoAccess — membership revogada impede acesso.
  - TestTenantIsolation_MultiTenant — 3 tenants com 2 users cada, isolamento validado.
- Requer: OMNIRA_DATABASE_URL definida e migrations aplicadas (tables criadas, RLS enabled).
- Testes contra PostgreSQL real em CI (não mocks).

### Added (T06 — Authorization + TenantContext)
- `internal/tenancy/domain/context.go`: `TenantContext` (TenantID, ActorID, AccessSource) com factory + context helpers.
- `AccessSource` enum (direct, hub, system) para rastreabilidade de origem da requisição.
- `internal/tenancy/application/authorization.go`: `AuthorizationService` validando membership ativo.
- `AuthorizeAccessToTenant(ctx, tenantID, actorID)`: valida tenant ativo + membership ativo, retorna TenantContext.
- `IsAuthorized(ctx, tenantID, actorID)`: predicate boolean para checks simples.
- **Regra crítica**: `tenant_id` do request NUNCA é aceito como autoridade; validado SOMENTE via membership.
- `internal/tenancy/adapters/http.go`: `AuthorizationMiddleware` integrando authn → authz → TenantContext.
- Middleware encadeia: Principal (authn) → AuthorizeAccessToTenant → WithTenantContext.
- Respostas HTTP: 200 OK (authorized), 403 Forbidden (denied), 404 Not Found (tenant not found), 401 Unauthorized (missing Principal).
- Testes: 5 cenários autorização (success, no membership, inactive membership, inactive tenant, IsAuthorized).
- Testes HTTP adapter: Missing Principal → 401, Missing tenant_id → 400.

### Tested (T03–T13)

#### R0.1 Release Gate — PASSED ✓
- **54 unit + integration tests** (T01–T11)
- **5 adversarial isolation tests** (T12)
- **1 backup/restore validation drill** (T13)
- **Total: 60 tests, 100% pass rate**
- **Test suite coverage**: authn, config, tenancy (domain, app, adapters), audit, outbox, OTel, isolation, DR
- **Real database validation**: migrations applied, RLS enabled, data integrity verified
- **Metrics**:
  - Unit test latency: ~350ms
  - Backup time: 106ms
  - Restore time: 102ms (RTO)
  - Backup size: 44K
- **Docker build**: omnira-api 7.9MB, omnira-worker built
- **CI-ready**: all tests can run in CI against PostgreSQL

### Tested (T03–T12)
- ✓ Migrations aplicam sem erro (4 migrations = 8 tabelas).
- ✓ RLS habilitado em todas as 8 tabelas.
- ✓ 9 permissões seed.
- ✓ 5 system roles seed (tenant_admin, tenant_supervisor, tenant_agent, system_admin, hub_admin).
- ✓ Schema validation passa.
- ✓ Testes de isolamento validam tenant boundary.
- ✓ `go test ./internal/tenancy/domain` — 7 testes (Tenant/Membership state machine).
- ✓ `go test ./internal/tenancy/application` — 5 testes (AuthorizationService scenarios).
- ✓ `go test ./internal/audit/application` — 5 testes (RecordEvent, EventMetadata, GetEventsByCorrelation, ListTenantEvents, ListEventsByAction).
- ✓ `go test ./internal/outbox/application` — 5 testes (RecordEvent, GetUnpublishedEvents, MarkPublished, RecordAttempt, ListTenantEvents).
- ✓ `go test ./internal/platform/otel` — 5 testes (GeneratesCorrelationID, PreservesCorrelationID, GetCorrelationID_Missing, WithCorrelationID, StatusCode).
- ✓ `go test ./internal/tenancy/adapters` — 17 testes (HTTP handlers, HTTP middleware, RLS isolation, Postgres persistence, **4 isolation adversarial**).
- ✓ `go test ./internal/platform/authn` — 3 testes (JWT validation, middleware).
- ✓ `go test ./internal/platform/config` — 4 testes (config loading, validation).
- ✓ `go test ./apps/api/cmd/omnira-api` — 3 testes (health endpoints, graceful shutdown).
- ✓ `go build ./apps/api/cmd/omnira-api` compila sem erros (7.9MB).
- ✓ **Todos os testes passam: 54 testes, 0 falhas, ~350ms total (inclui DB real)**.
- ✓ Testes Postgres (T07): OMNIRA_DATABASE_URL definida, migrations aplicadas, conexões reais.
- ✓ Testes HTTP (T08): handlers testados sem servidor, validam JSON, status codes, middleware chain.
- ✓ Testes Audit (T09): correlation tracking, metadata, tenant isolation validados.
- ✓ Testes Outbox (T10): transactional recording, unpublished queries, attempt tracking validados.
- ✓ Testes OTel (T11): correlation ID generation, preservation, context injection, status code capture validados.
- ✓ **Testes Isolation (T12): adversarial Tenant A vs B, membership revocation, multi-tenant separation, tenant status enforcement validados contra PostgreSQL real**.
