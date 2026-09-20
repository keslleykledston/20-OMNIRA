# 07 CONTACTS LIST

**Rota:** `/app/contacts`  
**Referência visual:** `../references/06-contacts-list.png`  
**Papéis:** Admin, Supervisor, Agent conforme permissão.

## Propósito

CRM leve para localizar contatos omnichannel, visualizar canais/tags, atividade e tickets.

## Wireframe

```text
APP SHELL
└─ PAGE
   ├─ Header + New contact
   ├─ 3 summary metrics
   ├─ Search + filters
   └─ Contacts table + pagination
```

## Layout e regiões

- Três metric cards na primeira linha.
- Search ocupa maior parte da segunda linha; filtros Tag/Canal/Último contato/Responsável à direita.
- Tabela com avatar/nome/email na primeira coluna, canais como pequenos badges/ícones.

## Funcionalidades e interações

Busca por nome, telefone ou email; opcional documento quando suportado.  
Filtros de tag, canal, última interação, responsável e status.  
Clique abre Contact 360.  
Menu de row: iniciar conversa, criar ticket, editar, adicionar tag (só se autorizado).

## Dados necessários

ContactSummary: id, name, email, phone, channels[], tags[], lastInteractionAt, lastInteractionChannel, openTicketCount, status. Metrics: total, activeInPeriod, withOpenTicket.

## Estados obrigatórios

Loading table/metrics, empty filtered, error, permission. Search deve ter debounce e estado refletido na URL quando possível.

## Responsividade

Tablet reduz filtros a Filter Sheet. Mobile usa cards com nome, telefone, tags, última interação e tickets.

## Component map conceitual

`MetricCard`, `SearchField`, `FilterBar`, `ContactRow`, `ChannelBadge`, `Tag`, `Pagination`, `DropdownMenu`.

## Checklist de fidelidade

- metric cards com ícones suaves;
- ícones de canais pequenos e legíveis;
- tags em pills suaves;
- tabela arejada, row height consistente;
- paginação igual à de Tickets.
