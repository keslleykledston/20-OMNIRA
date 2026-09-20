# 05 TICKETS LIST

**Rota:** `/app/tickets`  
**Referência visual:** `../references/08-tickets-list.png`  
**Papéis:** Admin, Supervisor, Agent conforme escopo/filas.

## Propósito

Fila operacional de tickets com busca, filtros, prioridades, atribuição e navegação para detalhe.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header + New ticket
   ├─ Quick status segments
   ├─ Advanced filter row
   └─ Ticket table + pagination
```

## Layout e regiões

- Quick filters ocupam uma linha de tabs/segments.
- Filtros avançados em cards/controls compactos abaixo.
- Tabela full-width com row height ~58–68 px.
- Cliente inclui avatar + canal em sublinha.
- Assunto pode ter preview secundário.

## Funcionalidades e interações

Filtros rápidos: Todos, Meus, Minha fila, Abertos, Aguardando, Resolvidos.  
Avançados: status, prioridade, fila, responsável, canal, categoria, período.  
Ordenação por última atualização.  
Row clicável; menu de ações por linha.  
Bulk selection só se backend/UX suportar ação real; caso contrário remover checkbox.

## Dados necessários

Ticket: id, createdAt, contact, subject, excerpt, status, priority, queue, assignee, channel, updatedAt. Pagination preferencialmente cursor no backend; UI pode representar page state.

## Estados obrigatórios

Loading: table skeleton. Empty: contexto do filtro + limpar filtros. Error: retry. Permission: apenas tickets autorizados; frontend não assume escopo.

## Responsividade

Tablet esconde excerpt e/ou fila; mobile usa card rows com ID, contato, assunto, status, prioridade, updatedAt.

## Component map conceitual

`SegmentedControl`, `FilterBar`, `Select`, `Table`, `TicketRow`, `StatusBadge`, `PriorityBadge`, `Pagination`, `DropdownMenu`.

## Checklist de fidelidade

- cabeçalho/tabs/filtros alinhados;
- status e prioridade como pills suaves;
- alta densidade sem ficar espremido;
- menu `…` no extremo direito;
- paginação no rodapé direito.
