# 06 TICKET DETAIL

**Rota:** `/app/tickets/[ticketId]`  
**Referência visual:** `../references/05-ticket-detail.png`  
**Papéis:** Admin, Supervisor, Agent autorizado.

## Propósito

Visualizar e atualizar um ticket, histórico, mensagens, anexos e contexto de cliente.

## Wireframe

```text
APP SHELL
└─ TICKET PAGE
   ├─ Back + ticket header + status action
   ├─ Tabs: Mensagens / Detalhes / Histórico / Anexos
   └─ Active tab
      ├─ Main details
      └─ Customer/context side card
```

## Layout e regiões

- Header compacto com ID, priority badge, assunto e status dropdown à direita.
- Tabs lineares.
- Detalhes em duas colunas: ~2/3 informações, ~1/3 cliente/tags/notas.
- Inputs editáveis usam o mesmo form pattern do design system.

## Funcionalidades e interações

Alterar status, prioridade, fila, responsável e categoria conforme permissão.  
`Ver contato` abre Contact 360.  
Tags editáveis.  
Observação interna nunca é enviada ao cliente.  
Histórico é timeline imutável de eventos.  
Anexos mostram preview/metadata/download autorizado.

## Dados necessários

Ticket full object + ContactSummary + tags + audit events + attachments. Atualizações usam optimistic UI apenas se rollback seguro.

## Estados obrigatórios

Not found, permission denied, loading, save in progress, save error, stale/conflict se API suportar versionamento.

## Responsividade

Mobile empilha as colunas; status permanece acessível no header; tabs horizontal-scroll se necessário.

## Component map conceitual

`PageHeader`, `Tabs`, `StatusBadge`, `PriorityBadge`, `Select`, `ContactCard`, `TagEditor`, `InternalNoteEditor`, `Timeline`.

## Checklist de fidelidade

- ID/priority e assunto devem ter hierarquia clara;
- painel do cliente compacto;
- controles editáveis não devem parecer formulário pesado;
- observação interna visualmente diferenciada.
