# ADR 0001 — TenantContext explícito e RLS

**Status:** Accepted
**Data:** 2026-09-18

## Contexto

O Hub BPO precisa operar múltiplos Tenants na mesma sessão sem permitir acesso implícito entre eles.

## Decisão

Usar `TenantContext` explícito na camada de aplicação. No tier padrão compartilhado, aplicar enforcement adicional no PostgreSQL com RLS/controle equivalente para tabelas tenant-owned. O mesmo contrato de domínio deve funcionar em placements dedicados.

Requests humanos usam `ActorID` de usuário autenticado e autorizado. Webhooks e workers usam `AccessSourceSystem` sem UUID de usuário fabricado: primeiro resolvem um registro persistido confiável (por exemplo, `ChannelConnection`), derivam dele o Tenant e só então abrem uma transação de sistema com `SET LOCAL` e RLS. Reserva idempotente e mutações causadas pelo mesmo evento devem compartilhar essa transação, para que falha faça rollback completo.

Payload assíncrono ou webhook nunca é autoridade para `tenant_id`. A elevação de sistema fica encapsulada no bootstrap/adapters e não é exposta a handlers de negócio genéricos.

## Consequências

Positivas:
- defesa em profundidade;
- testes de isolamento mais objetivos;
- reduz dependência de disciplina manual.

Custos:
- maior cuidado com jobs, migrations e queries administrativas;
- contexto precisa existir em toda execução assíncrona.
- operações de sistema precisam de entrada específica e auditável, distinta de usuário humano.

## Alternativas

- apenas filtro `WHERE tenant_id`: rejeitada para o MVP por depender demais de convenção.
- database dedicado por Tenant como padrão: rejeitado para todos os clientes no MVP; permanece disponível como tier Enterprise/BPO através de placement dedicado.
