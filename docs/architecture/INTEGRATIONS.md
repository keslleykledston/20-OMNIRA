# Integrações

## Princípio Anti-Corrupção

O core não conhece campos específicos de IXC/SGP/Hubsoft. Cada adapter implementa contratos canônicos.

## Porta canônica ERP

Operações MVP:

```text
find_subscriber(query)
list_invoices(subscriber_ref)
get_invoice_copy(invoice_ref)
open_support_ticket(subscriber_ref, category, summary)
get_support_ticket(ticket_ref)
```

## Resultado

Adapters retornam modelos canônicos e preservam `external_ref` separadamente.

## Resiliência

- timeout explícito;
- retry somente para operações idempotentes ou com idempotency key;
- circuit breaker;
- métricas por operação;
- DLQ para automações assíncronas;
- não mascarar erro externo como sucesso.

## Canais

Contrato canônico mínimo:
- ingest inbound;
- send text;
- send media;
- delivery status;
- read status quando suportado;
- identity mapping.

Capacidade específica do provedor deve ser declarada por feature flags/capabilities.
