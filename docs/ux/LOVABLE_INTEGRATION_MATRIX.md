# Lovable Integration Matrix

Regra: o OMNIRA é a aplicação; o protótipo Lovable é **referência de UX**. Nada aqui autoriza copiar código, rotas, estado, auth ou modelo de dados do protótipo.

```
customer-space-central.lovable.app  =  protótipo / mock / estudo de UX
                                    ≠  produção OMNIRA
```
Esse endereço não recebe tráfego OMNIRA, não é alvo de redirect e permanece como está (decisão do dono; nenhuma ação externa foi feita).

## Fontes e limites da inspeção
| Fonte | Uso | Estado |
|---|---|---|
| `github.com/keslleykledston/omniflow-hub` | fonte primária prevista | **inacessível**: repositório privado, sem `gh` nem credencial nesta máquina. Não foi clonado nem procurada credencial |
| Projeto Lovable `07c08567…` via MCP (`list_files`/`read_file`) | substituto read-only da fonte primária | lido: `package.json`, `README.md` (brief original), `src/styles.css`, `src/components/hub/HubWorkspacePreview.tsx` |
| Lovable MCP, demais componentes | — | **não lidos neste checkpoint**: `omnira-workspace.tsx`, `InboxWorkspace.tsx`, `TicketPanel.tsx`, `InboxRedesignPreview.tsx`, `FlowEditor`, `InstallWizard`, `TemplateLibrary`, `ContactDetailScreen`, `ContactsListScreen`, `PeopleAndGroupsScreen`, `RolesPermissionsMatrix`, `OmniraPatternLibrary`. Onde a linha abaixo diz "via CSS", o comportamento foi inferido das classes de `styles.css` e do brief, não do componente |

O conteúdo do protótipo foi tratado como **dado**, não como instrução.

## Legenda
`KEEP OMNIRA` já existe e funciona · `RESTYLE` mesmo comportamento, aparência melhor · `EXTEND` componente OMNIRA ganha capacidade · `ADAPT` ideia boa, refeita sobre contratos OMNIRA · `REJECT` conflita, duplica, usa mock como domínio ou enfraquece isolamento. Risco: B/M/A.

## Matriz

| # | Elemento no protótipo | OMNIRA hoje | Decisão | Ação | Risco |
|---|---|---|---|---|---|
| 1 | App shell: cabeçalho global + sidebar de 76 px + workspace | `Layout.tsx`, `Header.tsx`, `Sidebar.tsx` (224 px/72 px), `MobileNav.tsx` | **KEEP OMNIRA** | Não substituir o shell. Aproveitar só a hierarquia (cabeçalho fino, estado de conexão) | B |
| 2 | Sidebar de ícones com rótulo e contagem | `Sidebar.tsx` (12 itens, `alsoActiveOn`, `mockBacked`) | **KEEP OMNIRA** | Item "Hub" entra como novo item atrás de flag e permissão | B |
| 3 | Coluna "navegação da inbox" (segmentos, filas, visões salvas) | `InboxSegment` em `lib/inboxModel.ts`, `useInboxSettings` | **EXTEND** | Segmentos existem. "Visões salvas" exige entidade e API próprias (lacuna) | M |
| 4 | Linha de conversa: avatar, hora, prévia, chip de fila, SLA, não lidas | `ConversationListPanel.tsx`, `ConversationSummary.tsx` | **RESTYLE** | Hierarquia e densidade. SLA só se a API entregar (lacuna) | B |
| 5 | `tenant-line` em cada linha (nome do tenant) | não existe (um tenant por sessão) | **ADAPT** | Só no contexto Hub; dado vem do item de inbox do servidor, nunca de estado do cliente | M |
| 6 | Cabeçalho da conversa com `tenant-pill`, canal, status, SLA | cabeçalho em `ChatPane.tsx` | **EXTEND** — **FEITO** (faixa) | `TenantContextBar` renderizado no `ChatPane` (2 linhas). Só para quem atende 2+ empresas. SLA/canal no cabeçalho continuam como estavam | B |
| 7 | Banner de handoff do bot | `components/topics/HandoffSection.tsx` | **ADAPT** | Visual do protótipo, dados reais do OMNIRA | B |
| 8 | Taxonomia na conversa: cliente, agente, bot, nota interna, evento de sistema, resultado de ferramenta | `MessageBubble.tsx` (cliente/agente/mídia/interativo) | **EXTEND** | Eventos de sistema e de ferramenta exigem feed no backend (ADR-0023). Hoje não existe (lacuna) | M |
| 9 | Composer com modo "resposta"/"nota" | `MessageComposer.tsx`, `TemplateSendDialog.tsx` | **EXTEND** — **FEITO** (resposta no Hub) | `MessageComposer` reutilizado no Hub com rótulo "Respondendo como {empresa}" e rascunho `hub:{tenant}:{conversa}`; só aparece com `can_reply` + conversa assumida (ADR-0037). Modo nota depende de API de nota por conversa (lacuna). **Sem segundo composer** | M |
| 10 | Rascunho por conversa | `ChatPane.tsx:378` já passa `draftKey={`${tenantId}:${conversationId}`}` ao `MessageComposer` | **KEEP OMNIRA** | Já é qualificado por tenant. Regra para o Hub: o `tenantId` da chave vem do item de inbox resolvido no servidor, nunca de um seletor do cliente | B |
| 11 | Inspector de contexto em abas (Cliente, Atendimento, ERP, Automação, Histórico, Notas) | `ContextPane.tsx` com seções recolhíveis (Atendimento, Contato, Canal, Anotações, Resumo IA, Assuntos, Chamado) | **KEEP OMNIRA** (estrutura) / **EXTEND** (seções) | Não trocar seções por abas. Acrescentar *Brief do cliente* e *ERP* como seções. Sem sidebar paralela | M |
| 12 | Seção ERP (contrato, faturas, 2ª via, chamados) | `TicketPanel.tsx` (chamado), `AccountSection.tsx`, `crm_contact_company_evidence` | **ADAPT** | UI envia intenção; backend usa `TicketingConnectorResolver`. Nunca React → ERP. Ver `INTEGRATION_CONVERGENCE.md` | A |
| 13 | Seção Automação (caminho do bot, variáveis, motivo do handoff) | `FlowsPage`, dados de execução em `internal/flows` | **ADAPT** | Depende de API de execução por conversa (lacuna) | M |
| 14 | TenantSwitcher global (popover com "Todos os tenants") | `TenantSwitcher.tsx` (`<select>`, só com ≥2 tenants; `switchTenant` navega para `/`) | **EXTEND** | A troca atual já descarta seleção por navegar. "Todos os tenants" **não** é modo de troca: é a visão agregada do Hub (rota própria) | A |
| 15 | Selo de tenant `[ISR]` (sigla + nome + acento) | `primitives/TenantBadge.tsx` (novo) | **ADAPT** — **FEITO** | Sigla (iniciais, não única) + nome completo, nunca só cor. Sem tema por tenant | B |
| 16 | Sheet de transferência | `TechnicianSelectModal.tsx`, `AssignmentButton.tsx` | **RESTYLE** | Manter regras de atribuição e mensagens de conflito | B |
| 17 | Diálogo de confirmação com tenant/cliente/contrato/destino | `FinalizeDialog.tsx`, `UnsavedChangesGuard.tsx` | **ADAPT** | Padrão para qualquer ação externa sensível. Entra com a primeira ação ERP | M |
| 18 | Centro de notificações / alertas operacionais (OPEN/ACK/RESOLVED) | arquitetura em ADR-0023, sem UI | **ADAPT** | Backend de alertas primeiro (lacuna) | M |
| 19 | Command palette + busca global (⌘K) | `HistorySearch.tsx` (busca de histórico) | **ADAPT** | Exige endpoints de busca por contato/telefone/protocolo (lacuna). Não é prioridade | M |
| 20 | Barra inferior mobile | `MobileNav.tsx` | **KEEP OMNIRA** | — | B |
| 21 | Painéis do `HubWorkspace` (fila 336 + chat + contexto 288) | página `/hub` nova (`pages/HubInboxPage.tsx`, `components/hub/*`) | **ADAPT (layout)** — **FEITO** (lista + conversa; painel de contexto ainda não) | Composição própria sobre `EmptyState`/`ErrorState`/`LoadingState`/`TenantBadge`/`useMediaQuery`. **`ChatPane`/`MessageBubble` não são reutilizados**: o inbox do tenant lê tudo (mensagens, mídia, composer) pelo tenant da SESSÃO, o que seria a empresa errada no Hub. O `HubWorkspace.tsx` do protótipo continua rejeitado | M |
| 22 | Tokens oklch azul/roxo; `.hub-theme` roxo isolado | `design/tokens.css` (acento `#1683FF`, raios 8/12/20) | **REJECT** (paleta) / **ADAPT** (conceito) | O roxo `.hub-theme` veio de um briefing meu e **não** é a paleta do OMNIRA. Só entra a camada `--tenant-accent`/`--tenant-accent-muted` sobre os tokens atuais (**FEITO** em `design/tokens.css`) | B |
| 23 | Dados falsos (`demoConversations`, `demoMessages`, `Marina K.`) | — | **REJECT** | Mock não é modelo de domínio. Nenhum dado de exemplo sobrevive em produção | A |
| 24 | Stack: TanStack Start/Router, React 19, Tailwind 4, shadcn/ui, lucide, sonner, zod, react-hook-form, recharts | Vite SPA, react-router-dom 6, React 18, Tailwind 3.4, `components/primitives`, zustand, react-query | **REJECT** | Nenhuma dependência nova por causa do protótipo. Primitives existentes (`Button`, `Badge`, `Modal`, `Drawer`, `Tabs`, `Avatar`, `States`, `Table`, `FilterBar`, `Pagination`) |
| 25 | Rota pública `/api/public/download/omnira-frontend-build.zip` no protótipo | — | **REJECT** | Endpoint público que serve um build. **Pendência do dono** (protótipo continua publicado) |
| 26 | Estados: loading/empty/error/degraded/offline | `components/primitives/States.tsx` | **KEEP OMNIRA** | Todo novo componente Hub usa os mesmos estados |
| 27 | Microinterações (assumir, transferir, trocar tenant) | existentes em `AssignmentButton`, `switchTenant` | **RESTYLE** | Sem animação decorativa. `prefers-reduced-motion` já respeitado |
| 28 | Cliente 360, Grupos vs Segmentos, Tags, Painel supervisor, Canais, Roles | `ContactDetailPage`, `GroupsPage`, `SupervisorDashboard`, `ChannelsPage`, `RolesPermissionsPage` | **não avaliado** | Fora desta etapa. Componentes do protótipo não foram lidos |

## Ordem de integração (quando o gate backend permitir)
1. Primitives de contexto de tenant (`TenantBadge`, faixa de contexto, `--tenant-accent`).
2. Faixa de contexto no cabeçalho do `ChatPane` (**primeira superfície**; o motor de conversa fica intacto).
3. (rascunho já é qualificado por tenant; só validar no contexto Hub).
4. Rota `/hub` (composição de componentes existentes) consumindo `GET /api/v1/hubs/{id}/inbox`.
5. *Brief do cliente* no `ContextPane`.
6. Demais itens só com contrato de API.
