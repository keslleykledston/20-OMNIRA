# Rotas e organização por feature

## Rotas

```text
/app/dashboard
/app/conversations
/app/tickets
/app/tickets/[ticketId]
/app/contacts
/app/contacts/[contactId]
/app/channels
/app/channels/whatsapp/new
/app/channels/[connectionId]
/app/automation
/app/automation/[flowId]
/app/reports
/app/settings/general
/app/settings/team
/app/settings/queues
/app/settings/sla
/app/settings/integrations
/app/settings/security
/app/settings/audit
```

## Estrutura sugerida

Preservar a estrutura real do repo se já existir padrão. Conceitualmente:

```text
apps/web/features/
  dashboard/
  conversations/
  tickets/
  contacts/
  channels/
  automation/
  reports/
  settings/

packages/ui/
  tokens/
  primitives/
  components/
  patterns/
```

## Data adapters

Cada feature expõe interface de leitura/comando. `fixture-adapter` para telas antes do backend; `api-adapter` para endpoints Go. Components não sabem qual adapter está ativo.
