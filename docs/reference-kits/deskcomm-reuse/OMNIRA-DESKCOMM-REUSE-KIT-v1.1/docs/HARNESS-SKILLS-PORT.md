# Harness / Skills

## Fonte

Estudar:

```text
AGENTS.md
CLAUDE.md
.agents/skills/
tests/invariants/
scripts/
.github/workflows/
```

## Objetivo

Não copiar o harness inteiro.

Extrair padrões úteis para:
- automation-first;
- source-of-truth docs;
- invariant tests;
- release gates;
- migration gates;
- packaging checks;
- agent handoff.

## Skills OMNIRA sugeridas

```text
tenant-isolation-review
migration-review
whatsapp-meta-review
channel-contract-review
ios-ui-review
release-review
docker-runtime-review
dr-drill
```

Criar somente quando houver uso real.

## Tools OMNIRA sugeridas

```text
tools/check-rls
tools/test-isolation
tools/check-migrations
tools/check-openapi
tools/check-asyncapi
tools/test-whatsapp-contract
tools/docker-smoke
tools/doctor
```

## Regra

Deskcomm-specific paths ou commands devem ser reescritos.
Nunca copiar uma skill que diga ao agente usar Supabase/WAHA/Caddy como regra do OMNIRA.
