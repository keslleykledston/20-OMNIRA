# 01 DASHBOARD

**Rota:** `/app/dashboard`  
**Referência visual:** `../references/01-dashboard.png`  
**Papéis:** Admin, Supervisor; Agent pode receber versão reduzida conforme permissões.

## Propósito

Visão geral operacional do tenant ativo: volume, tickets, tempo de resposta e atalhos para filas de trabalho.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header: greeting + period
   ├─ Metrics grid: 4 cards
   ├─ Analytics row
   │  ├─ Conversations by channel
   │  └─ Ticket status donut
   └─ Activity row
      ├─ Recent conversations
      └─ Priority tickets
```

## Layout e regiões

- Conteúdo desktop começa ~24–32 px após a sidebar.
- Header compacto; seletor de período no canto superior direito.
- KPIs em 4 colunas iguais, 110–130 px de altura.
- Segunda e terceira linhas usam grid 1:1.
- Cards brancos, borda sutil e radius 12–14 px.
- Gráficos não devem dominar a tela; altura ~260–300 px.

## Funcionalidades e interações

- Seletor: Hoje, Últimas 24h, 7 dias, 30 dias, Personalizado.
- Clique em KPI navega com filtro correspondente.
- Clique em série/canal filtra Inbox.
- Clique em status do donut filtra Tickets.
- Conversa recente abre a conversa.
- Ticket prioritário abre detalhe do ticket.

## Dados necessários

KPIs: active_conversations, new_contacts, open_tickets, avg_first_response, trend/comparison.  
Channel series: channel, count, period.  
Ticket status: status, count.  
Recent conversations: contact, preview, channel, timestamp, unreadCount.  
Priority tickets: id, subject, contact, priority, status.

## Estados obrigatórios

Loading: skeleton para KPIs, charts e listas.  
Empty: cards permanecem com zero + mensagens úteis.  
Error: erro por widget quando possível, sem derrubar dashboard inteiro.  
Permission: ocultar widgets não autorizados.

## Responsividade

Desktop 4 KPIs; tablet 2x2; mobile 1 coluna ou 2 colunas compactas. Charts empilham no mobile. Listas viram cards compactos.

## Component map conceitual

`PageHeader`, `DateRangePicker`, `MetricCard`, `Card`, `StatusBadge`, `ConversationRow`, `TicketRow`, chart wrapper.

## Checklist de fidelidade

- greeting e período alinhados como referência;
- quatro cards com mesma altura;
- cores de ícones suaves e diferentes por métrica;
- dashboard visualmente leve, sem gridlines fortes;
- listas inferiores mantêm densidade compacta.
