# 04 INBOX CONVERSATIONS

**Rota:** `/app/conversations`  
**Referência visual:** `../references/04-inbox-conversations.png`  
**Papéis:** Admin, Supervisor, Agent; ações respeitam RBAC e ownership/fila.

## Propósito

Workspace principal de atendimento omnichannel para localizar, ler, responder, transferir e contextualizar conversas.

## Wireframe

```text
APP SHELL
└─ INBOX WORKSPACE
   ├─ ConversationList (~320–350)
   ├─ ChatPane (flex)
   │  ├─ Header
   │  ├─ Timeline
   │  └─ Composer
   └─ ContextPane (~290–330)
```

## Layout e regiões

- Altura disponível = viewport menos shell; evitar scroll da página inteira.
- ConversationList e Timeline têm scroll independente.
- Divisórias hairline, surfaces brancas.
- Header do chat fixo no painel; composer fixo na base.
- Context pane também scrollável se necessário.

## Funcionalidades e interações

ConversationList: segmented `Todas / Não lidas / Meus`, busca e filtros.  
Row: avatar, nome, canal, preview, hora, unread e seleção.  
Chat header: contato, número, canal, ticket, transferir, resolver, menu.  
Timeline: inbound esquerda, outbound direita; datas; status send/delivered/read/failed.  
Composer: Resposta/Ações, textarea auto-grow, emoji/anexo/quick reply, Enter envia, Shift+Enter quebra linha.  
Ações: transferir, mudar fila, criar ticket, tag, tool ERP quando disponível, finalizar.  
Context: perfil, ticket atual, dados, tags, integrações futuras.

## Dados necessários

Conversation summary: id, contact, channel, status, queue, assignee, lastMessage, unreadCount, updatedAt.  
Message: id, direction, type, content/media, timestamp, deliveryStatus, sender.  
Context: ContactSummary + active TicketSummary + tags.  
Filtros: channel/status/queue/assignee/tag/unread/period.

## Estados obrigatórios

No selection: empty center state. Loading list/timeline independently. Provider disconnected: banner + composer disabled, histórico continua. Send failure: inline retry. Permission: actions disabled/hidden + backend 403 handled.

## Responsividade

Desktop: 3 painéis. Tablet: list + chat, contexto em Sheet. Mobile: list → chat; contexto/ticket em full-height Sheet; bottom nav. Preserve composer e header.

## Component map conceitual

`SegmentedControl`, `SearchField`, `FilterBar`, `ConversationRow`, `MessageBubble`, `MessageComposer`, `ContactCard`, `TicketCard`, `Sheet`, `ProviderBanner`.

## Checklist de fidelidade

- painel selecionado em azul muito claro;
- mensagens inbound/outbound claramente distintas sem cores fortes;
- densidade da lista semelhante ao mock;
- scrolls independentes;
- composer nunca some ao rolar mensagens;
- painel direito não domina o chat.
