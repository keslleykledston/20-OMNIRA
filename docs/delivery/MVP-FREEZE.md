# MVP Freeze — Contrato de Implementação

**Status:** PARTIALLY FROZEN — isolamento e backend resolvidos. R0.1 Tenant Foundation pode iniciar.

## Backend

- Core/API/workers: Go.
- Referência: ADR 0008.

## Isolation Tier

- MVP: Shared Strong Isolation.
- Enterprise/BPO futuro: Dedicated Database via Tenant Placement.
- Referência: ADR 0005.

## Incluído
- Identity/Tenant/Membership/RBAC;
- Hub BPO e grants;
- TenantContext + enforcement de persistência;
- WhatsApp Oficial;
- Webchat;
- inbox/ticket/fila;
- roteamento round-robin;
- IXC com 5 operações;
- supervisor básico;
- automação mínima;
- observabilidade e auditoria.

## Excluído
Tudo listado como “Fora do MVP” em `docs/product/MVP.md`.

## Política de mudança
Depois de `FROZEN`, qualquer adição exige:
1. remover item de custo equivalente; ou
2. registrar decisão explícita de extensão de prazo/escopo.

Nenhuma feature entra por “já que estamos aqui”.
