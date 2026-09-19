# Port Guide — Tenancy / RLS

## Fonte Deskcomm a estudar

```text
docs/specs/01-spec-platform-base.md
tests/invariants/rls-isolation.test.ts
tests/invariants/rls-completude-varredura.test.ts
docs/specs/13-spec-governanca-atendimento.md
```

## Não copiar

- `auth.users`;
- Supabase-specific helpers;
- service_role semantics;
- tenant arrays no JWT como autorização principal.

## Reusar

- conceito de isolamento por `tenant_id`;
- coverage scan de tabelas tenant-owned;
- prova comportamental A/B;
- audit append-only;
- membership revocation;
- RBAC server-side.

## OMNIRA invariants

Toda tabela tenant-owned:
- `tenant_id NOT NULL`;
- FK apropriada;
- índice começando por `tenant_id` quando útil;
- RLS;
- prova automatizada.

O teste de completude deve falhar se uma nova tabela com `tenant_id` for adicionada sem:
- policy;
- ou prova explícita de exceção.

## Testes adversariais

```text
A reads A
A cannot read B
A cannot update B
A cannot delete B
A cannot enumerate B
known B UUID is useless to A
revoked membership denies immediately
role escalation denied
cross-tenant join does not leak
search/list/export are tenant scoped
```
