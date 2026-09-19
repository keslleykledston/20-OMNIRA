# Ondas de reaproveitamento

## Wave D0 — Audit only

Objetivo:
- clonar/fixar DeskcommCRM como referência externa;
- registrar SHA;
- não copiar código ainda;
- gerar inventário.

Saídas:
- `docs/research/deskcomm/SOURCE-MAP.md`
- `docs/research/deskcomm/REUSE-AUDIT.md`

## Wave D1 — Tenancy Harness

Extrair:
- RLS completeness patterns;
- tenant A/B adversarial tests;
- membership revocation cases;
- append-only audit concepts.

OMNIRA target:
- `tests/isolation/`
- `tools/check-rls`
- `.agents/skills/tenant-isolation-review/`

Gate:
cross-tenant suite verde.

## Wave D2 — Channel Seam

Extrair:
- ChannelProvider/ChannelAdapter concepts;
- capabilities model;
- tenant-scoped channel reference;
- provider-specific IDs.

OMNIRA target:
- `internal/channels/`

Sem Meta ainda.
Primeiro criar seam e testes.

## Wave D3 — WhatsApp Meta Official

Extrair do Deskcomm:
- Meta Cloud adapter behavior;
- inbound ingest edge cases;
- webhook duplicate handling;
- media download safety;
- health checks;
- template concepts;
- phone variants.

Implementar em Go.

Gate:
- inbound text;
- outbound text;
- status;
- idempotência;
- tenant separation;
- webhook verification;
- media safe download.

## Wave D4 — Inbox / Contacts

Adaptar frontend:
- 3-pane operational layout;
- mobile one-column navigation;
- ConversationList;
- ChatThread;
- Composer;
- CRM side panel;
- filters;
- loading/empty/error.

Obrigatório:
- Omnira iOS Design System;
- sem Supabase client no feature code;
- API Go.

## Wave D5 — Assignment / Routing

Portar:
- claim;
- release;
- transfer;
- assignment history;
- agent availability;
- capacity;
- round-robin;
- queue semantics.

Gate:
race condition claim test + audit.

## Wave D6 — Supervisor

Portar/adaptar:
- queue metrics;
- per-agent metrics;
- dashboard lite;
- read-only supervisor observation rules quando aplicável.

## Wave D7 — Automation

Portar:
- event-driven rules;
- condition evaluation;
- action registry;
- graph validation;
- anti-loop;
- run history.

Adaptar:
- visual editor.

OMNIRA async:
Outbox → NATS → automation worker.

## Wave D8 — Harness/Skills

Incorporar ideias úteis de:
- `.agents/skills`;
- AGENTS/CLAUDE conventions;
- release checks;
- invariant gates;
- packaging checks.

Não copiar regras Deskcomm específicas sem adaptação.
