# Component Mapping

Do protótipo Lovable (referência) para o OMNIRA existente. Nada aqui cria um componente com nome "Lovable/New/2". Novos primitives só onde **não há equivalente**.
Legenda de decisão: KEEP · RESTYLE · EXTEND · ADAPT · REJECT (ver `LOVABLE_INTEGRATION_MATRIX.md`).
Arquivos citados existem em `web/src/` (verificados na branch).

| Conceito de UX | Referência (protótipo) | OMNIRA hoje | Decisão | Fonte de dados (API real) | Contexto de segurança | Nota de integração (sem substituição) |
|---|---|---|---|---|---|---|
| Shell global | `app-shell`, `global-header`, `sidebar` | `Layout.tsx`, `Header.tsx`, `Sidebar.tsx`, `MobileNav.tsx` | KEEP | `GET /me`, `GET /tenants/{t}/me/access` | permissões em `lib/permissions.ts`, `lib/useAccess.ts` | Item "Hub" atrás de flag + permissão de membro de hub |
| Troca de tenant | `tenant-trigger`, popover | `TenantSwitcher.tsx` + `lib/tenants.ts` | EXTEND | `GET /tenants` (`useMyTenants`) | lista vem do servidor; `switchTenant` navega para `/` | Não vira modo "todos". A visão agregada é a rota `/hub` |
| Selo de tenant | `tenant-pill`, `tenant-avatar` | — | ADAPT → novo primitive `TenantBadge` | nome/sigla do tenant do item resolvido no servidor | só exibe; nunca decide acesso | Sigla + nome + `--tenant-accent`; nunca só cor |
| Lista de conversas | `conversation-list`, `conversation-row` | `ConversationListPanel.tsx`, `ConversationSummary.tsx` | RESTYLE | `GET /tenants/{t}/inbox/conversations` | RLS por membership | Mesma fonte; no Hub a lista vem de `GET /hubs/{h}/inbox` |
| Cabeçalho da conversa | `conversation-header`, `conversation-status` | cabeçalho em `ChatPane.tsx` | EXTEND | `GET .../inbox/conversations/{c}` | idem | **Primeira superfície**: faixa de contexto do tenant, sem tocar no motor de conversa |
| Timeline | `timeline`, `message`, `system-event`, `tool-event` | `ChatPane.tsx`, `MessageBubble.tsx`, `MessageMedia.tsx` | EXTEND | `GET .../messages`, SSE `.../events` | idem | Eventos de sistema/ferramenta pedem feed novo (lacuna) |
| Banner de handoff | `handoff` | `topics/HandoffSection.tsx` | ADAPT | dados de tópico/handoff existentes | idem | Só visual |
| Composer | `composer`, `composer-mode` | `MessageComposer.tsx`, `TemplateSendDialog.tsx` | EXTEND | `POST .../messages`, anexos ADR-0024 | `conversation.claim`/responsável | **Um** composer. "Respondendo como [tenant]" é texto derivado do contexto efetivo |
| Painel de contexto | `context-inspector`, `context-tabs` | `ContextPane.tsx`, `CollapsibleSection.tsx` | KEEP (estrutura) / EXTEND (seções) | `.../conversations/{c}/ticket`, `contacts/{id}`, `accounts/{id}` | idem | Seções novas: Brief do cliente, ERP |
| Seção ERP | `invoice`, `integration-head` | `TicketPanel.tsx`, `topics/TicketSection.tsx`, `AccountSection.tsx` | ADAPT | `.../conversations/{c}/ticket*` (hoje só chamado) | ação externa via backend e conector | 2ª via/OS: **sem API ainda** (lacuna) |
| Transferência | `transfer-sheet` | `TechnicianSelectModal.tsx`, `AssignmentButton.tsx` | RESTYLE | `GET /tenants/{t}/users/agents`, queues | RBAC `conversation.manage` | Preservar mensagens de conflito |
| Confirmação sensível | `confirm-dialog` | `FinalizeDialog.tsx`, `components/primitives/Modal.tsx` | ADAPT | — | tenant/cliente/contrato/destino vêm do servidor | Padrão para a 1ª ação ERP |
| Estados vazio/erro/carregando | skeleton, empty | `primitives/States.tsx` | KEEP | — | — | Obrigatório em toda tela nova |
| Filtros e paginação | `filter-strip` | `primitives/FilterBar.tsx`, `primitives/Pagination.tsx`, `lib/inboxModel.ts` | KEEP | cabeçalhos `X-Pagination-*` | — | O endpoint Hub segue o mesmo contrato |
| Notificações | `notifications-list` | — (ADR-0023, sem UI) | ADAPT (futuro) | SSE `.../inbox/events` | — | Depende de backend de alertas |
| Busca global / ⌘K | `global-search`, `command-input` | `HistorySearch.tsx` | ADAPT (futuro) | sem endpoint de busca global (lacuna) | — | Não é prioridade |
| Barra mobile | `mobile-tabs` | `MobileNav.tsx` | KEEP | — | — | — |
| Layout 3 painéis do Hub | `hub-panels` | composição de `ConversationListPanel` + `ChatPane` + `ContextPane` | ADAPT (layout) / REJECT (componente) | `GET /hubs/{h}/inbox`, `.../{item}` | RLS + `HubAuthorizationService` | Rota nova `/hub`, usando `useMediaQuery` + `Drawer` existentes |
| Tokens | `styles.css` (oklch) | `design/tokens.css`, `design/typography.css`, `tailwind.config.js` | REJECT (paleta) / ADAPT (conceito) | — | — | Apenas `--tenant-accent`, `--tenant-accent-muted` |
