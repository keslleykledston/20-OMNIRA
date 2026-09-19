# Modelo de Dados Conceitual

## Núcleo

```text
User
Membership
Tenant
Hub
HubTenantGrant
Role
Permission

Contact
ChannelConnection
Conversation
Message
Ticket
Queue
Department
Assignment
SlaPolicy

IntegrationConnection
ExternalCustomerRef
ExternalTicketRef

AutomationFlow
AutomationVersion
AutomationRun

AuditEvent
DomainEvent
```

## Invariantes

- `Contact.tenant_id` obrigatório.
- `Conversation.tenant_id` obrigatório.
- `Ticket.tenant_id` obrigatório.
- `Message.tenant_id` derivado e persistido para enforcement/particionamento.
- `Queue`, `Department`, `ChannelConnection`, `IntegrationConnection` pertencem a um Tenant.
- `HubTenantGrant` concede acesso, não move dados.
- FKs tenant-owned devem preferir validação composta `(tenant_id, id)` quando aplicável.

## IDs

Usar UUID/ULID internos. CNPJ, telefone, ids de ERP e ids de provedor são atributos/identificadores externos, nunca chave primária de autorização.
