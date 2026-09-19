# Acceptance Gates

## Qualquer port backend

- Go compile;
- tests;
- Docker build;
- no Supabase runtime dependency;
- TenantContext;
- RLS quando tenant-owned;
- OTel;
- OpenAPI/AsyncAPI;
- changelog.

## WhatsApp

- official Meta path;
- webhook auth;
- idempotency;
- retry classification;
- media SSRF protection;
- tenant-scoped credentials;
- no token in NATS/logs;
- health/degraded state.

## Frontend

- Omnira iOS Design System;
- no direct DB;
- no Supabase feature dependency;
- responsive;
- keyboard;
- WCAG AA;
- loading/error/empty;
- tenant context visible.

## Routing

- atomic claim;
- append-only assignment history;
- race test;
- tenant isolation.

## Automation

- versioned;
- deterministic validation;
- anti-loop;
- run history;
- retries via worker/tool runtime where relevant.

## Provider não oficial

Além dos gates normais:

- provider classificado explicitamente como `unofficial`;
- opt-in por Tenant;
- risco exibido na UI;
- aceite auditado;
- session storage isolado;
- admin/dashboard do provider não público;
- health/reconnect observável;
- secrets/session tokens não vazam para logs/NATS;
- nenhum fallback silencioso official → unofficial.
