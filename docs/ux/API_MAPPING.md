# API Mapping

Só APIs **existentes** e lacunas **reais**. Nenhuma API foi inventada porque o mock esperava uma forma. Rotas verificadas em
`internal/platform/httpserver/server.go` e `contracts/openapi/omnira-v1.yaml` (branch `fix/integrate-lovable-into-omnira`).
Semântica de tenant: nas rotas de tenant o `tenant_id` do caminho é **reivindicação**, validada por membership (`AuthorizationMiddleware`) + RLS.
Nas rotas Hub o tenant **nunca** vem do cliente.

| Superfície | Dados necessários | API existente | DTO existente | Autorização | Semântica de tenant | Capacidade de backend faltando | Decisão |
|---|---|---|---|---|---|---|---|
| Troca de tenant | tenants do usuário | `GET /tenants` | `MyTenant` (`lib/tenants.ts`) | sessão autenticada; RLS | só tenants com membership ativa | — | usar a existente |
| Lista de conversas (tenant) | conversas, prévia, não lidas | `GET /tenants/{t}/inbox/conversations` | `ConversationPage` | membership + RLS | um tenant por chamada | SLA contratual por linha: **não existe** (só `wait_warn/danger_minutes`, "display-only") | manter; SLA = lacuna |
| Cabeçalho + faixa de contexto | conversa, tenant, canal | `GET .../inbox/conversations/{c}`, `GET .../{c}/channel` | `ConversationItem` | membership + RLS | tenant do caminho | nome/sigla do tenant atual: já em `GET /tenants/{t}` | usar existentes |
| Mensagens + tempo real | mensagens, eventos | `GET .../{c}/messages`, SSE `.../{c}/events`, `.../inbox/events` | `MessagePage` / `MessageItem` | membership + RLS | tenant do caminho | eventos de sistema/ferramenta na timeline | **lacuna** (ADR-0023) |
| Envio | texto, template, anexo | `POST .../messages`, anexos (ADR-0024) | — | `conversation.claim`/responsável; `Source=direct` | tenant efetivo | — | usar; Hub **não** escreve |
| Painel de contexto: chamado | ticket vinculado | `GET /tenants/{t}/conversations/{c}/ticket[/{id}]` | (ver OpenAPI `.../ticket`) | `ticket.read` | tenant do caminho | — | usar |
| Painel de contexto: conta/ERP | conta, empresa | `GET /tenants/{t}/accounts[/{id}]`, `.../crm/companies` | `CustomerAccount` | `account.read` | tenant do caminho | faturas, contrato, 2ª via, abrir OS: **não existem** | **lacuna** (ver `INTEGRATION_CONVERGENCE.md`) |
| Notas internas da conversa | notas | só de contato: `GET/POST /tenants/{t}/contacts/{c}/notes` | `ContactNote` | membership | tenant do caminho | nota por conversa | **lacuna** |
| Automação do atendimento | caminho do bot, variáveis, motivo do handoff | `/flows/*` (editor/execuções) | (ver OpenAPI `/flows`) | `flow.view` | tenant do caminho | execução por conversa | **lacuna** |
| Transferir | agentes, filas | `GET /tenants/{t}/users/agents`, `.../queues` + atribuição existente | (ver OpenAPI `users/agents`) | `conversation.manage` | tenant do caminho | — | usar |
| Meus hubs | hubs ativos de que sou membro, com o meu papel | **`GET /hubs`** | `HubList` (novo, HTTP VERIFIED) | sessão autenticada; só as minhas linhas | nenhuma (membro de hub não dá acesso a tenant) | — | usar; 404 = Hub desligado no servidor |
| Inbox do Hub (lista) | itens do tenant autorizado | **`GET /hubs/{hub_id}/inbox`** | `HubInboxPage` / `HubInboxItem` (novo, HTTP VERIFIED) | membro ativo do hub; RLS filtra por grant/contrato/fila | **derivado no servidor**; `tenant_id` na query → 400; `X-Tenant-ID` ignorado | projetor existe (`OMNIRA_HUB_PROJECTOR_ENABLED`, desligado, reconciliação periódica); `sla_due_at` sem fonte | usar; flag `OMNIRA_HUB_API_ENABLED` |
| Abrir item do Hub | conversa + mensagens do item | **`GET /hubs/{hub_id}/inbox/{item_id}`** | `HubInboxItemDetail` (novo, HTTP VERIFIED) | `HubAuthorizationService` (membro, grant, contrato, escopo de fila) | tenant e fila lidos da **linha persistida** | contexto de cliente/ERP; tempo real do Hub | usar; somente leitura |
| Responder / atribuir pelo Hub | — | **nenhuma** | — | exigiria `Source=hub` aceito em `messages/send` etc. | — | policies de escrita + serviço + testes próprios | **BLOCKED** (fatia futura) |
| Provisionar hub/contrato/grant | — | **nenhuma HTTP**; ferramenta de operador `omnira-hubctl` (`docs/ops/HUB-PROVISIONING.md`) | — | operador no servidor; auditado | — | API HTTP exige o conceito de administrador de plataforma, que não existe | **lacuna deliberada** |
| Alertas / notificações | alertas OPEN/ACK/RESOLVED | SSE de inbox/presença | — | — | — | entidade e API de alertas | **lacuna** |
| Busca global | contato/telefone/CPF/protocolo | `GET .../contacts?q=`, `HistorySearch` | — | membership | por tenant | busca unificada | **lacuna** |

## Estados obrigatórios por tela nova
Loading (skeleton), vazio, erro, **negado/404 uniforme**, degradado/reconectando. O 404 do Hub é intencionalmente igual para hub inexistente, não-membro, sem grant, revogado e fora de escopo.
