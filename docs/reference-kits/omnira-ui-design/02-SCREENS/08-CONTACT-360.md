# 08 CONTACT 360

**Rota:** `/app/contacts/[contactId]`  
**Referência visual:** `../references/07-contact-360.png`  
**Papéis:** Admin, Supervisor, Agent autorizado.

## Propósito

Visão 360 do contato: identidade, histórico omnichannel, tickets, tags e dados de CRM.

## Wireframe

```text
APP SHELL
└─ CONTACT PAGE
   ├─ Back + identity header + actions
   ├─ Tabs
   └─ Overview
      ├─ Metrics row (4)
      ├─ Conversation history
      ├─ Recent tickets
      └─ Right column
         ├─ Contact profile
         ├─ Customer data
         └─ Notes
```

## Layout e regiões

- Header com avatar grande, nome, telefone, canal, cliente desde e ações à direita.
- Tabs: Visão geral, Conversas, Tickets, Dados, Atividade.
- Overview: coluna principal ~70%, lateral ~30%.
- Quatro cards compactos no topo da coluna principal.

## Funcionalidades e interações

Ações: iniciar conversa, criar ticket, editar, adicionar tag.  
Conversas mostram canal, direção/preview e data.  
Tickets recentes clicáveis.  
Perfil permite copiar telefone/email.  
Dados/Notas editáveis conforme permissão; Activity é timeline.

## Dados necessários

Contact full object, channel identities, tags, metrics, recent conversations, recent tickets, custom fields, notes, activity events.

## Estados obrigatórios

Loading parcial; contato sem histórico; dados faltantes; permission; conflict de edição. Campos PII devem seguir política de acesso.

## Responsividade

Tablet empilha lateral abaixo do conteúdo; mobile ações vão para menu e tabs scrolláveis.

## Component map conceitual

`ContactHeader`, `MetricCard`, `ConversationHistoryRow`, `TicketRow`, `ContactProfileCard`, `TagEditor`, `NotesCard`, `Tabs`.

## Checklist de fidelidade

- avatar/header como foco;
- quatro metrics iguais;
- lateral com cards empilhados;
- canais usam iconografia sem dominar;
- histórico e tickets usam linhas discretas, sem caixas excessivas.
