# Adaptação do Frontend Deskcomm

## O que pode ser reaproveitado

Principalmente estrutura/comportamento de:

```text
app/app/inbox/
components/inbox/
app/app/contacts/
app/app/crm/
app/app/kanban/
app/app/connections/
```

Antes de copiar:
- localizar dependências Supabase;
- localizar hooks de auth;
- localizar realtime;
- localizar API routes internas.

## Regra

Componentes visuais puros podem ser COPY/ADAPT.

Hooks/Data access são quase sempre ADAPT/REWRITE.

## OMNIRA frontend target

```text
apps/web/
  features/
    tenancy/
    contacts/
    inbox/
    tickets/
    integrations/
    automation/
    supervisor/
    hub/

packages/ui/
```

## iOS Design System obrigatório

Todo componente adaptado deve:
- usar tokens OMNIRA;
- usar componentes `packages/ui`;
- remover branding/cores Deskcomm;
- seguir navegação OMNIRA;
- preservar teclado e acessibilidade;
- suportar loading/error/empty;
- suportar responsive/mobile.

## Proibido

- importar `@supabase/*` em feature components;
- chamar DB direto do browser;
- usar Deskcomm como visual final;
- manter ícones/bibliotecas múltiplas sem necessidade;
- usar `organization_id` do client como prova de autorização.

## Data access

```text
React component
 -> OMNIRA API client
 -> /api/v1
 -> Go
```

Realtime:

```text
REST initial state
+
OMNIRA WebSocket/SSE invalidation/events
```
