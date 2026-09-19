# ADR 0001 — TenantContext explícito e RLS

**Status:** Proposed  
**Data:** 2026-09-18

## Contexto

O Hub BPO precisa operar múltiplos Tenants na mesma sessão sem permitir acesso implícito entre eles.

## Decisão

Usar `TenantContext` explícito na camada de aplicação. No tier padrão compartilhado, aplicar enforcement adicional no PostgreSQL com RLS/controle equivalente para tabelas tenant-owned. O mesmo contrato de domínio deve funcionar em placements dedicados.

## Consequências

Positivas:
- defesa em profundidade;
- testes de isolamento mais objetivos;
- reduz dependência de disciplina manual.

Custos:
- maior cuidado com jobs, migrations e queries administrativas;
- contexto precisa existir em toda execução assíncrona.

## Alternativas

- apenas filtro `WHERE tenant_id`: rejeitada para o MVP por depender demais de convenção.
- database dedicado por Tenant como padrão: rejeitado para todos os clientes no MVP; permanece disponível como tier Enterprise/BPO através de placement dedicado.
