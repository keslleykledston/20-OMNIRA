# AppShell

## Wireframe

```text
┌──────────────────┬────────────────────────────────────────────────┐
│ OMNIRA           │                                                │
│                  │             PAGE CONTENT                       │
│ Dashboard        │                                                │
│ Conversas        │                                                │
│ Tickets          │                                                │
│ Contatos         │                                                │
│ Canais           │                                                │
│ Automação        │                                                │
│ Relatórios       │                                                │
│ Configurações    │                                                │
│                  │                                                │
│ Tenant/User      │                                                │
└──────────────────┴────────────────────────────────────────────────┘
```

## Desktop

- sidebar: 210–240 px;
- fixed/sticky 100vh;
- conteúdo ocupa restante;
- canvas geral cinza-azulado muito claro;
- sidebar branca com hairline à direita;
- item ativo usa primary-soft, texto/ícone azuis.

## Tenant card

Sempre no rodapé da sidebar:
- avatar/logo;
- nome do tenant;
- role do usuário;
- abre switcher se multi-tenant existir.

## Tablet

Sidebar compacta 64–72 px ou drawer, conforme padrão existente. Labels podem ocultar; tooltip obrigatório.

## Mobile

Bottom navigation com quatro slots principais: Dashboard, Conversas, Tickets, Mais. Configurações, Contatos, Canais, Automação e Relatórios ficam em Mais.

## PageHeader

- título + descrição à esquerda;
- ação primária à direita;
- evita header muito alto;
- filtros globais podem ocupar segunda linha.
