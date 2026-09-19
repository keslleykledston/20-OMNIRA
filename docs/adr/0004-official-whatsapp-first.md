# ADR 0004 — WhatsApp Oficial como canal principal do MVP

**Status:** Proposed  
**Data:** 2026-09-18

## Contexto

O MVP precisa validar atendimento real com menor risco jurídico/operacional de integração não oficial.

## Decisão

Priorizar WhatsApp Business Platform/API oficial via adapter. Webchat entra como canal controlado para testes e fallback.

## Consequências

- onboarding depende do ecossistema Meta/BSP;
- templates e regras do provedor precisam ser modelados;
- evita basear o core em automação de WhatsApp Web.
