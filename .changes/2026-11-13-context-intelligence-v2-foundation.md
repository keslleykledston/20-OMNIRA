# Context Intelligence v2 — foundation through CONTEXT.2D round 7 fixes (feature not enabled)

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
- CONTEXT.2D round 1: independent review (`codex exec review --base c394452`, the diff accumulated over 2A–2C)
  found and this branch fixed 3 real correctness bugs: a pointer-into-growing-slice bug that silently dropped
  evidence for earlier facts whenever an analysis had 2+ facts; `Claim` ignoring `due_at`, which defeated `Retry`'s
  exponential backoff; and a redelivered message landing in a brand-new window once its original window had
  already closed, double-counting and double-analyzing it (fixed by making `(tenant_id, message_id)` the real
  primary key of `context_analysis_window_messages`, migration 000176 — edited in place, not yet merged/deployed).
  Also fixed, found while writing 2D's own test-gap coverage (not a Codex finding): a 2C test's wrong assumption
  that `tenant_agent` lacks `account.read` (migration 000073 seeds it by default), and added the Hub-delegated
  HTTP path test for `GetContextView` deferred from 2C.
- CONTEXT.2D round 2: a second independent review found 3 more real issues (none from round 1 re-flagged), all
  fixed: the worker could not recover idempotently from a crash between persisting an analysis and marking its
  window complete (fixed by checking for an existing receipt before doing any work, so a reclaimed window
  recognizes it and never calls the model again); the frontend panel never refetched after an async analysis
  completed (fixed with a conditional poll while stale); and staleness was computed by comparing timestamps,
  which misses a message sharing the exact same instant as the analyzed one or an older message delivered
  out-of-order into a later window (fixed with a real message/window-membership check instead).
- CONTEXT.2D round 3: a third independent review found 3 more real issues, none from rounds 1-2 re-flagged: the
  new analysis tables' RLS hid every row from a Hub-delegated actor despite the handler already admitting the
  request (fixed with additive delegated-read policies, migration 000177, mirroring the existing contacts/media
  pattern; also switched the facts-to-topic_threads join to LEFT so a fact is never dropped for a delegated
  actor even though topic_threads itself still lacks any delegated policy — a separate, pre-existing,
  out-of-scope gap from ADR-0017); a summary over 200 characters could repeatedly fail topic creation and
  dead-letter the window (fixed by bounding the generated title); and the frontend panel stopped polling for
  good once caught up, missing later messages (fixed by invalidating its query on the same realtime event that
  already refreshes messages/timeline).
- CONTEXT.2D round 4: a fourth independent review found 2 more real issues, none from rounds 1-3 re-flagged: a
  worker connection-pool exhaustion risk (every window analysis opened a second, nested database session on top
  of the one the runner already holds, which alone could deadlock the worker once concurrent analyses matched
  the pool size — fixed by reusing the already-active session's connection instead) and a media-only window
  being saved as a permanently "empty" receipt before its transcript/description finished, so that content would
  never actually be analyzed (fixed by retrying instead of completing while any message's media analysis is
  still pending).
- CONTEXT.2D round 5: a fifth independent review found 3 more issues. The AI-usage ledger write inside the window
  analyzer had the SAME nested-connection risk round 4 fixed for MessageIDs (a pre-existing pattern also present,
  unfixed, in 4 other already-shipped intelligence features — out of scope to fix there; fixed here only, via a
  per-window pending-usage collector the runner flushes after releasing its own session). A window mixing a text
  message with a still-pending media message skipped the pending-media check entirely (round 4 only checked when
  a window had zero usable text) and could permanently omit that media once finalized — fixed by checking for
  the whole window up front. And context_analysis_windows/context_analysis_window_messages (migration 000176)
  had the same delegated-read RLS gap round 3 fixed for the analysis tables (migration 000177), making a fully
  analyzed conversation read as permanently stale for a Hub-delegated actor — fixed with the same additive
  policies.
- CONTEXT.2D round 6: a sixth independent review found 2 more P2 issues. The pending-media check only looked at
  message_media_analysis, missing the window where an image/PDF is still being virus-scanned or already clean
  but not yet picked up by the periodic vision-enqueue job (audio's transcript row is created synchronously by a
  trigger instead, so it never had this gap) — fixed by also checking message_media directly, without waiting
  forever for media neither pipeline ever analyzes. And "the latest analysis" (and previous_analysis_id's chain)
  ordered by created_at, which an older window's late-finishing retry could win back over a newer generation that
  already completed — fixed by ordering by the window's own generation instead.
- CONTEXT.2D round 7: a seventh independent review found 2 more P2 issues (plus a parameter-binding typo the
  integration suite itself caught in round 6's own fix, fixed alongside). The pending-media check would wait
  forever for a tenant that never opted into vision, or a deployment with transcription turned off, since
  neither pipeline would ever produce a terminal outcome on its own — fixed by only waiting on a pipeline the
  analyzer is told is actually live (WithMediaPipelines, defaulting to "both live" unless configured, matching
  main.go's own OMNIRA_WHISPER_URL/OMNIRA_MULTIMODAL_ANALYSIS_ENABLED flags). And a non-empty caption on an
  image/PDF/audio made the window permanently discard the completed media analysis instead of combining both.
- Documented architecture, current reuse of existing intelligence/service-context modules, implementation gaps, and mandatory safety/deploy gates.
- Schema, routing and API changed (additive migrations, two new tenant-scoped endpoints); still no production
  configuration or rollout change — `OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED` stays `false` everywhere.
