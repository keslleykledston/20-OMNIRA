# Context Intelligence v2 — foundation through CONTEXT.2C (feature not enabled)

- Added pure adaptive 30s/120s/12-message batching policy, forced-flush events, and unit tests.
- Added strict AI proposal parser with topic/evidence alias allowlists and no operational side effects.
- CONTEXT.2A: added `context_analysis_windows`/`context_analysis_window_messages` (migration 000176) and the
  Postgres-backed scheduling store/runner (sweep/claim/lease, same pattern as the existing intelligence job queue)
  plus a dedicated worker consumer for the per-conversation message-persisted event.
- CONTEXT.2B: added `context_window_analyses`/`context_proposed_facts`/`context_proposed_fact_evidence` (migration
  000177, with an immutability trigger) and the `WindowAnalyzerService` that turns a closed window into typed,
  evidence-linked proposals (need/observation/commitment/suggested_action/possible_resolution/risk) — never applied
  automatically, always `ai_inferred` on creation, accounted in the existing AI usage ledger.
- CONTEXT.2C: added a server-only read model (`GET .../inbox/conversations/{id}/context-view`) over CONTEXT.2B's
  analyses, and an idempotent, rate-limited manual refresh (`POST .../context-analysis/refresh`, the real call-site
  of CONTEXT.2A's `Force`/manual trigger). Reading requires `topic.read` and is reachable in a Hub-delegated
  session through its `conversation.read` grant (ADR-0040); `needs_customer_account_selection` is present only for
  an actor who also holds `account.read`. Wired into `ContextPane` as a new, collapsible "Inteligência de
  contexto" section (manual refresh button, staleness badge, read-only facts) — no operator confirm/dismiss flow
  yet (CONTEXT.2D). OpenAPI updated (`ContextView`/`ContextFact` schemas, both new operations).
- Documented architecture, current reuse of existing intelligence/service-context modules, implementation gaps, and mandatory safety/deploy gates.
- Schema, routing and API changed (additive migrations, two new tenant-scoped endpoints); still no production
  configuration or rollout change — `OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED` stays `false` everywhere.
