# ADR 0003 — Eventos para integrações e efeitos assíncronos

**Status:** Proposed  
**Data:** 2026-09-18

## Contexto

Webhooks, status de mensagem, automações e ERPs possuem latência, retries e falhas independentes.

## Decisão

Comandos síncronos atualizam estado transacional; efeitos externos e fan-out usam eventos/jobs com idempotência.

## Consequências

- melhor resiliência;
- necessidade de idempotency keys, retries e DLQ;
- eventual consistency deve ser refletida na UI.
