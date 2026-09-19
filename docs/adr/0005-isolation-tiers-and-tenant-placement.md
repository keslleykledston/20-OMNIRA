# ADR 0005 — Isolation Tiers e Tenant Placement

**Status:** Accepted  
**Data:** 2026-09-18

## Contexto

A OMNIRA precisa equilibrar custo e simplicidade operacional do SaaS com requisitos de clientes Enterprise/BPO que podem exigir isolamento físico de banco.

Uma promessa única de “banco isolado” seria ambígua: pode significar isolamento lógico forte ou database fisicamente dedicado.

## Decisão

Adotar dois níveis de isolamento sob o mesmo modelo de domínio:

### Shared Strong Isolation
Tier padrão:
- infraestrutura PostgreSQL compartilhada;
- `tenant_id` obrigatório;
- `TenantContext`;
- RLS/controle equivalente;
- auditoria;
- testes adversariais de isolamento.

### Dedicated Database
Tier Enterprise/BPO:
- database dedicado por Tenant ou boundary contratualmente definido;
- mesmos contratos de domínio, autorização e APIs;
- mesma semântica de Hub e Membership;
- provisioning e placement tratados como infraestrutura.

A aplicação deverá resolver persistência através de um `Tenant Placement`, evitando que módulos de domínio dependam da topologia física.

## Consequências

Positivas:
- custo eficiente no tier padrão;
- opção de compliance/isolamento físico;
- evita fork de produto;
- permite migração de Tenant entre placements.

Custos:
- connection/pool management futuro é mais complexo;
- migrations precisam suportar múltiplos placements;
- observabilidade e backup precisam identificar placement;
- operações administrativas cross-tenant exigem ainda mais controle em ambiente híbrido.

## Regras comerciais

- “Shared Strong Isolation” não pode ser vendido como “database dedicado”.
- “Dedicated Database” só pode ser prometido quando o placement correspondente estiver provisionado.
- contratos e propostas devem nomear o tier explicitamente.

## Alternativas

- schema por Tenant: não adotado como tier principal.
- database dedicado para todos: rejeitado por custo/complexidade prematuros.
- somente banco compartilhado para sempre: rejeitado por limitar Enterprise/BPO.
