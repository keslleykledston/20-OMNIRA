# Reuse First

## Regra

Antes de criar qualquer arquivo de UI:

```text
REUSE
  ↓
EXTEND
  ↓
CREATE
```

## Auditoria recomendada

Pesquisar por nome, responsabilidade e aparência:

```bash
rg -n "AppShell|Sidebar|Button|Card|Badge|Tabs|Table|Sheet|Modal|Skeleton|EmptyState|ConversationRow|TicketCard|ChannelCard" apps packages components features . 2>/dev/null
find .agents/skills -maxdepth 3 -type f -print 2>/dev/null
```

## Critério de extensão

Estender um componente existente se:

- representa a mesma responsabilidade;
- a nova variante não altera sua semântica;
- pode ser resolvida por prop/token/slot sem acoplamento excessivo.

Criar novo componente se a responsabilidade for realmente distinta.
