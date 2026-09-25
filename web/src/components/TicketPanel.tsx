import React, { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId } from '../lib/session';
import {
  createExternalTicket,
  readConversationTicket,
  refreshConversationTicket,
  updateConversationTicketStatus,
  providerStatusOptions,
  type ConversationTicketReadResponse,
  type ExternalTicketCreateResponse,
  type ExternalTicketProblem,
} from '../lib/tickets';
import { Button, ConfirmDialog } from './primitives';

interface Company {
  id: string;
  name: string;
  cnpj?: string;
}

interface TicketPanelProps {
  conversationId: string;
  crmContactId?: string;
}

const FIELD =
  'w-full min-w-0 rounded-control border border-border-subtle bg-surface px-3 py-2 text-xs text-text-primary ' +
  'placeholder:text-text-tertiary focus:outline-none focus-visible:ring-2 focus-visible:ring-accent-primary ' +
  'disabled:opacity-50 disabled:cursor-not-allowed';

const SECTION_TITLE = 'mb-2 text-[10px] font-bold uppercase text-text-tertiary';

// PRODUCT.6-N: creating a real external ticket (PRODUCT.6-M) is a single,
// non-retryable-by-us write against K3G — the Idempotency-Key and the
// outcome of the last attempt must survive a page reload, or reloading
// mid-flow could send a second real CreateTicket with a fresh key. This
// is frontend-only state (no backend read endpoint exists for
// "does this conversation already have an external ticket" that a
// tenant_agent — who only has ticket.create, not ticket.read, PRODUCT.6-F
// — is authorized to call), so it is persisted client-side per
// conversation rather than re-derived from the server.
type Phase = 'idle' | 'submitting' | 'created' | 'replayed' | 'already_linked' | 'reconciliation_required' | 'error';

interface PersistedState {
  idempotencyKey: string;
  subject: string;
  description: string;
  selectedCompanyId: string;
  phase: Phase;
  result: ExternalTicketCreateResponse | null;
  problem: ExternalTicketProblem | null;
  errorMessage: string | null;
}

function storageKey(conversationId: string): string {
  return `omnira.ticket-create.${conversationId}`;
}

function freshState(): PersistedState {
  return {
    idempotencyKey: crypto.randomUUID(),
    subject: '',
    description: '',
    selectedCompanyId: '',
    phase: 'idle',
    result: null,
    problem: null,
    errorMessage: null,
  };
}

function loadState(conversationId: string): PersistedState {
  try {
    const raw = localStorage.getItem(storageKey(conversationId));
    if (raw) return { ...freshState(), ...JSON.parse(raw) };
  } catch {
    // Corrupt/unavailable storage: fall back to a fresh intent rather than
    // block the panel — the backend's own idempotency remains the real
    // safety net either way.
  }
  return freshState();
}

function saveState(conversationId: string, state: PersistedState): void {
  try {
    localStorage.setItem(storageKey(conversationId), JSON.stringify(state));
  } catch {
    // Best-effort only.
  }
}

// PRODUCT.6-O1F: server-truth read of the conversation's ticket projection
// (PRODUCT.6-O1, local-only, no provider call). This decides whether the
// CREATE form may be offered at all — localStorage (PersistedState above)
// is create-intent UX persistence ONLY and is never authoritative for
// whether an external link already exists (PRODUCT.6-M5 remains the real
// duplicate-prevention boundary regardless of what this panel renders).
type ReadPhase = 'loading' | 'linked' | 'unlinked' | 'not_found' | 'inconsistent' | 'forbidden' | 'unavailable' | 'network_error';

// PRODUCT.6-O1RF: explicit, user-initiated provider projection refresh.
// Deliberately component state, never persisted — refresh has no
// CREATE-style intent that must survive a reload (section 14: localStorage
// never becomes a second source of projection truth). Idle on every fresh
// mount/conversation switch; only ever changes in response to a click.
type RefreshState = 'idle' | 'pending' | 'reconciliation' | 'unavailable' | 'forbidden' | 'network_error';

// PRODUCT.6-O2BF: a status-mutation intent this browser confirmed but could
// not confirm the outcome of (a POST that failed with no HTTP response at
// all — section 14). Persisted ONLY at that moment, separately from the
// CREATE flow's own storage key/shape (a different concern, a different
// endpoint) — never proactively persisted before every confirm, since an
// ordinary 409 (a DIFFERENT, unrelated unresolved attempt blocking a brand
// new key) must never be mistaken for THIS intent being ambiguous.
interface PendingStatusIntent {
  tenantId: string;
  conversationId: string;
  localTicketId: string;
  provider: string;
  externalTicketId: string;
  targetStatus: string;
  idempotencyKey: string;
}

type MutationPhase =
  | 'idle'
  | 'pending'
  | 'network_ambiguous'
  | 'reconciliation_required'
  | 'unprocessable'
  | 'forbidden'
  | 'not_found'
  | 'unavailable';

function statusIntentStorageKey(conversationId: string): string {
  return `omnira.ticket-status.${conversationId}`;
}

function loadStatusIntent(conversationId: string): PendingStatusIntent | null {
  try {
    const raw = localStorage.getItem(statusIntentStorageKey(conversationId));
    if (!raw) return null;
    return JSON.parse(raw) as PendingStatusIntent;
  } catch {
    return null;
  }
}

function saveStatusIntent(conversationId: string, intent: PendingStatusIntent | null): void {
  try {
    if (intent === null) {
      localStorage.removeItem(statusIntentStorageKey(conversationId));
    } else {
      localStorage.setItem(statusIntentStorageKey(conversationId), JSON.stringify(intent));
    }
  } catch {
    // Best-effort only.
  }
}

export function TicketPanel({ conversationId, crmContactId }: TicketPanelProps) {
  const tenantId = getTenantId();

  const [state, setState] = useState<PersistedState>(() => loadState(conversationId));

  const [readPhase, setReadPhase] = useState<ReadPhase>('loading');
  const [linkedTicket, setLinkedTicket] = useState<ConversationTicketReadResponse | null>(null);

  // Runs once per conversation: fetches the real O1 GET before rendering
  // anything create/linked-shaped, so the form never flashes on screen
  // only to be replaced a moment later (section 10).
  useEffect(() => {
    let active = true;
    setReadPhase('loading');
    setLinkedTicket(null);
    void (async () => {
      const result = await readConversationTicket(conversationId);
      if (!active) return;
      switch (result.kind) {
        case 'ok':
          if (result.data.linked) {
            setLinkedTicket(result.data);
            setReadPhase('linked');
          } else {
            setReadPhase('unlinked');
          }
          break;
        case 'not_found':
          setReadPhase('not_found');
          break;
        case 'inconsistent':
          setReadPhase('inconsistent');
          break;
        case 'forbidden':
          setReadPhase('forbidden');
          break;
        case 'unavailable':
          setReadPhase('unavailable');
          break;
        case 'network_error':
          setReadPhase('network_error');
          break;
      }
    })();
    return () => {
      active = false;
    };
  }, [conversationId]);

  // PRODUCT.6-O1RF state. refreshRequestIdRef is bumped on every new
  // refresh click AND on every conversation switch — a stale response
  // (section 15: a late refresh reply from conversation A arriving after
  // the panel has already moved on to conversation B) is detected by
  // comparing the id captured at request time against the ref's CURRENT
  // value when the response resolves, and simply discarded if they differ.
  const [refreshState, setRefreshState] = useState<RefreshState>('idle');
  const refreshRequestIdRef = useRef(0);
  // A ref, not React state, for the SAME reason inFlightRef exists above:
  // two clicks fired in the same tick both close over the same
  // pre-re-render refreshState, so state alone cannot reliably stop the
  // second one. Updates synchronously, before any await.
  const refreshInFlightRef = useRef(false);

  useEffect(() => {
    refreshRequestIdRef.current += 1;
    refreshInFlightRef.current = false;
    setRefreshState('idle');
  }, [conversationId]);

  const refreshTicket = async () => {
    if (refreshInFlightRef.current) return; // synchronous double-click guard
    refreshInFlightRef.current = true;
    const requestId = (refreshRequestIdRef.current += 1);
    setRefreshState('pending');
    const result = await refreshConversationTicket(conversationId);
    refreshInFlightRef.current = false;
    if (requestId !== refreshRequestIdRef.current) return; // superseded by a conversation switch or a newer click

    switch (result.kind) {
      case 'ok': {
        const next = result.data;
        // Section 6: provider + external_ticket_id are durable identity —
        // refresh must never look like "ticket replaced". The backend
        // already guards this server-side; this is a defensive frontend
        // check on top, not a substitute for it.
        if (
          linkedTicket &&
          (next.provider !== linkedTicket.provider || next.external_ticket_id !== linkedTicket.external_ticket_id)
        ) {
          setRefreshState('reconciliation');
          break;
        }
        setLinkedTicket(next);
        setRefreshState('idle');
        break;
      }
      case 'not_found':
      case 'reconciliation':
        setRefreshState('reconciliation');
        break;
      case 'forbidden':
        setRefreshState('forbidden');
        break;
      case 'unavailable':
        setRefreshState('unavailable');
        break;
      case 'network_error':
        setRefreshState('network_error');
        break;
    }
  };

  // PRODUCT.6-O2BF: external ticket STATUS MUTATION state — deliberately
  // separate from RefreshState above. "Atualizar" (refresh) and this
  // mutation are two distinct backend operations (section 16) and must
  // never share state, messaging, or triggers.
  const [selectedTarget, setSelectedTarget] = useState('');
  const [confirmOpen, setConfirmOpen] = useState(false);
  const [mutationPhase, setMutationPhase] = useState<MutationPhase>('idle');
  const [mutationNote, setMutationNote] = useState<string | null>(null);
  const [pendingIntent, setPendingIntent] = useState<PendingStatusIntent | null>(() => loadStatusIntent(conversationId));

  const statusRequestIdRef = useRef(0);
  const statusInFlightRef = useRef(false);

  useEffect(() => {
    statusRequestIdRef.current += 1;
    statusInFlightRef.current = false;
    setSelectedTarget('');
    setConfirmOpen(false);
    setMutationPhase('idle');
    setMutationNote(null);
    setPendingIntent(loadStatusIntent(conversationId));
  }, [conversationId]);

  // Section 10: a persisted intent may be reused only if the CURRENT server
  // projection still matches the exact identity it was recorded against.
  // Server projection always wins — this is re-checked on every render,
  // never trusted merely because it once loaded from storage.
  const canVerifyPendingIntent = Boolean(
    pendingIntent &&
      linkedTicket &&
      pendingIntent.tenantId === tenantId &&
      pendingIntent.conversationId === conversationId &&
      pendingIntent.localTicketId === linkedTicket.local_ticket_id &&
      pendingIntent.provider === linkedTicket.provider &&
      pendingIntent.externalTicketId === linkedTicket.external_ticket_id,
  );

  // Stale-intent cleanup: merely IGNORING a mismatched persisted intent
  // (via canVerifyPendingIntent above) is not enough — once the canonical
  // O1F projection has actually resolved, a stale intent must be deleted
  // from both component state and localStorage, never just left inert.
  // Guarded to run only after readPhase leaves 'loading': a request still
  // in flight carries no server authority yet, so nothing may be deleted
  // on its account (section 2).
  useEffect(() => {
    if (readPhase === 'loading' || !pendingIntent) return;

    if (readPhase === 'linked' && linkedTicket) {
      const matches =
        pendingIntent.tenantId === tenantId &&
        pendingIntent.conversationId === conversationId &&
        pendingIntent.localTicketId === linkedTicket.local_ticket_id &&
        pendingIntent.provider === linkedTicket.provider &&
        pendingIntent.externalTicketId === linkedTicket.external_ticket_id;
      if (!matches) {
        setPendingIntent(null);
        saveStatusIntent(conversationId, null);
      }
      return;
    }

    if (readPhase === 'unlinked' || readPhase === 'not_found') {
      // Canonical projection confirms there is no active external link at
      // all — a stale mutation intent can never be recovery authority for
      // a ticket the server no longer confirms as linked.
      setPendingIntent(null);
      saveStatusIntent(conversationId, null);
      return;
    }

    // inconsistent/forbidden/unavailable/network_error: no POSITIVE proof
    // the identity actually differs (matches existing O1F fail-closed
    // treatment of these outcomes) — conservatively RETAIN. This can never
    // establish linkage on its own: canVerifyPendingIntent above requires a
    // truthy linkedTicket, which none of these outcomes ever provide.
  }, [readPhase, linkedTicket, pendingIntent, conversationId, tenantId]);

  // runStatusMutation NEVER persists anything itself — the intent (section
  // 1) is always already durably saved by its caller BEFORE this function's
  // POST fires, whether that caller is a fresh confirm or a verify resend.
  // This function only ever CLEARS it (on a terminal/definitive outcome) or
  // leaves it exactly as-is (409 / network ambiguity — section 3/4).
  const runStatusMutation = async (targetStatus: string, idempotencyKey: string) => {
    if (statusInFlightRef.current || !linkedTicket) return; // synchronous double-click guard
    statusInFlightRef.current = true;
    const requestId = (statusRequestIdRef.current += 1);
    setMutationPhase('pending');

    const result = await updateConversationTicketStatus({ conversationId, targetStatus, idempotencyKey });

    statusInFlightRef.current = false;
    if (requestId !== statusRequestIdRef.current) return; // superseded by a conversation switch or a newer attempt

    const clearIntent = () => {
      setPendingIntent(null);
      saveStatusIntent(conversationId, null);
    };

    switch (result.kind) {
      case 'ok': {
        // Section 2: EVERY terminal 200 (normal, replayed, reconciled)
        // clears the intent — there is nothing left to verify or retry.
        const data = result.data;
        setLinkedTicket((prev) =>
          prev
            ? {
                ...prev,
                provider: data.provider,
                external_ticket_id: data.external_ticket_id,
                external_status: data.external_status,
                external_status_label: data.external_status_label,
                sync_status: data.sync_status,
                last_synced_at: data.last_synced_at ?? undefined,
              }
            : prev,
        );
        clearIntent();
        setSelectedTarget('');
        setMutationPhase('idle');
        setMutationNote(
          data.reconciled
            ? 'Status confirmado após verificação.'
            : data.replayed
              ? 'Alteração já confirmada.'
              : 'Status atualizado.',
        );
        break;
      }
      case 'reconciliation_required':
        // Section 3: the intent was already persisted before this request
        // fired — retain it exactly as-is. A 409 here may mean THIS SAME
        // key's own provider write is ambiguous and reconciliation could
        // not resolve it (O2BH: outcome_unknown persists under this exact
        // key), or that it is blocked by an earlier unresolved operation —
        // either way, "Verificar alteração" resending this SAME key is the
        // correct recovery action, never a new one.
        setMutationPhase('reconciliation_required');
        break;
      case 'network_error':
        // Section 4: already persisted before the POST fired — nothing to
        // add or change, only the visible phase changes.
        setMutationPhase('network_ambiguous');
        break;
      case 'unprocessable':
      case 'invalid':
        // 422 (idempotency mismatch OR provider-rejected target — the
        // backend gives no machine-readable way to tell them apart) is a
        // definitive client-consistency failure: never auto-retried, and
        // the intent it invalidates is cleared rather than reused.
        clearIntent();
        setMutationPhase('unprocessable');
        break;
      case 'forbidden':
        // Section 5: a definitive authorization failure — clear.
        clearIntent();
        setMutationPhase('forbidden');
        break;
      case 'not_found':
        // A definitive identity failure (no active ticket at all) — clear.
        clearIntent();
        setMutationPhase('not_found');
        break;
      case 'unavailable':
        // Section 5: every /ticket/status 503 path (missing service wiring
        // at the top of the handler, or TicketingRuntime resolution failure
        // — internal/tickets/application/update_external_ticket_status.go
        // resolves the runtime BEFORE ever calling Acquire) is confirmed,
        // from the backend's own source ordering rather than any error
        // string, to occur strictly before an attempt row can exist or a
        // provider call can happen. Never ambiguous: safe to clear.
        clearIntent();
        setMutationPhase('unavailable');
        break;
    }
  };

  // A genuinely NEW user intent: only reachable when no matching unresolved
  // intent already exists (confirmStatusMutation and the render below both
  // gate on !canVerifyPendingIntent) — so persisting here can never
  // overwrite an intent that still needs recovery (section 7).
  const confirmStatusMutation = async () => {
    if (!linkedTicket || canVerifyPendingIntent) return;
    const targetStatus = selectedTarget;
    const idempotencyKey = crypto.randomUUID();
    const intent: PendingStatusIntent = {
      tenantId,
      conversationId,
      localTicketId: linkedTicket.local_ticket_id,
      provider: linkedTicket.provider ?? '',
      externalTicketId: linkedTicket.external_ticket_id ?? '',
      targetStatus,
      idempotencyKey,
    };
    // Section 1: persist BEFORE the first POST. UX recovery evidence only —
    // never linkage/identity authority.
    setPendingIntent(intent);
    saveStatusIntent(conversationId, intent);
    // The dialog stays open (isPending disables both its actions, section
    // 22) until the request actually resolves — a same-tick second click
    // lands on an already-disabled Confirm button, and runStatusMutation's
    // own ref guard is the authoritative backstop either way.
    await runStatusMutation(targetStatus, idempotencyKey);
    setConfirmOpen(false);
  };

  // Section 15: the SAME target + SAME Idempotency-Key, never a new one —
  // this is a verification resend of an already-unresolved intent, not a
  // new user intent (section 8).
  const verifyPendingIntent = async () => {
    if (!pendingIntent) return;
    await runStatusMutation(pendingIntent.targetStatus, pendingIntent.idempotencyKey);
  };

  const providerOptions = linkedTicket ? providerStatusOptions(linkedTicket.provider) : null;
  const hasLinkedIdentity = Boolean(linkedTicket?.provider && linkedTicket?.external_ticket_id);
  const hasKnownProviderOptions = Boolean(providerOptions && providerOptions.length > 0);

  // An unresolved persisted intent must be dealt with (verified) before any
  // new, potentially conflicting intent is offered; 403 is a permanent
  // lock (section 19); mid-flight always locks (section 24).
  const statusControlsLocked =
    mutationPhase === 'pending' || mutationPhase === 'forbidden' || canVerifyPendingIntent;
  const alterarStatusDisabled =
    statusControlsLocked || !selectedTarget || selectedTarget === linkedTicket?.external_status;

  const currentStatusLabel = linkedTicket?.external_status_label || linkedTicket?.external_status || '';
  const targetStatusLabel = providerOptions?.find((o) => o.code === selectedTarget)?.label ?? selectedTarget;

  // PRODUCT.6-O1F FINAL AUTHORITY FIX: distinguishes a terminal phase that
  // was just produced by a REAL HTTP response received during this mount
  // (section 3: "fresh in-session create may continue updating the UI
  // immediately without an extra GET") from one merely LOADED from
  // localStorage (a prior session/browser tab, possibly stale). Only the
  // latter is subject to the contradiction check below — a fresh result is
  // definitionally in agreement with the current server state, since the
  // request that produced it just round-tripped to that same server.
  const freshLocalResultRef = useRef(false);

  // A genuinely new create intent starts only when the panel switches to a
  // different conversation — never merely because of a re-render, a
  // network retry, or a page reload of the SAME conversation.
  useEffect(() => {
    freshLocalResultRef.current = false;
    setState(loadState(conversationId));
  }, [conversationId]);

  useEffect(() => {
    saveState(conversationId, state);
  }, [conversationId, state]);

  const patch = (partial: Partial<PersistedState>) => setState((prev) => ({ ...prev, ...partial }));

  // R5.2/PRODUCT.6-K0: the trusted, provider-backed company source — the
  // ticket-create form below is the ONLY place this list feeds a browser
  // selection, so an arbitrary free-text external ID can never be
  // submitted through this UI.
  const [companies, setCompanies] = useState<Company[]>([]);
  const [loadingCompanies, setLoadingCompanies] = useState(false);

  useEffect(() => {
    const loadCompanies = async () => {
      setLoadingCompanies(true);
      try {
        // PRODUCT.7B1A: tenant-scoped company directory (was
        // /integrations/companies, authn-only, a confirmed P0 cross-tenant
        // leak — every tenant resolved through one global K3G client).
        const res = await axios.get(`${API_BASE}/tenants/${tenantId}/crm/companies`, { headers: authHeaders() });
        const items: Company[] = res.data.items || [];
        setCompanies(items);
      } catch (err) {
        console.error('Erro ao carregar empresas:', err);
        // Not critical: the panels show "Nenhuma empresa disponível".
      } finally {
        setLoadingCompanies(false);
      }
    };
    void loadCompanies();
  }, []);

  const submitting = state.phase === 'submitting';
  const finished = state.phase === 'created' || state.phase === 'replayed';
  const alreadyLinked = state.phase === 'already_linked';
  const blocked = state.phase === 'reconciliation_required';

  // PRODUCT.6-O1F FINAL AUTHORITY FIX: localStorage may assert that this
  // conversation's ticket is externally linked (created/replayed/
  // already_linked, persisted from a PRIOR successful create in THIS
  // browser). That assertion is UX diagnostic evidence only — it is NEVER
  // authoritative over a successful server GET. If the canonical GET
  // (readPhase resolved to 'unlinked' or 'not_found') contradicts it, the
  // backend has no unlink capability, so this combination can only mean
  // the local evidence is stale (data restore, dev reset, manual repair,
  // reconciliation elsewhere) — fail closed: never show the stale linked
  // card, never offer CREATE automatically, never contact the provider.
  const localAssertsLinked = finished || alreadyLinked;
  const localServerContradiction =
    localAssertsLinked &&
    !freshLocalResultRef.current &&
    (readPhase === 'unlinked' || readPhase === 'not_found');
  const diagnosticExternalTicketId = state.result?.external_ticket_id ?? state.problem?.external_ticket_id ?? null;
  const diagnosticProvider = state.result?.provider ?? state.problem?.provider ?? null;

  // A ref, not React state, guards against a real double-click: two clicks
  // fired in the same tick both close over the same pre-re-render state,
  // so state.phase alone cannot reliably stop the second one from also
  // calling the backend. The ref updates synchronously, before any await.
  const inFlightRef = useRef(false);

  const submitTicket = async () => {
    if (inFlightRef.current || finished || alreadyLinked || blocked) return;
    if (!state.selectedCompanyId || !state.subject.trim() || !state.description.trim()) return;

    inFlightRef.current = true;
    patch({ phase: 'submitting', errorMessage: null });
    let outcome;
    try {
      outcome = await createExternalTicket(conversationId, state.idempotencyKey, {
        selected_customer_external_id: state.selectedCompanyId,
        subject: state.subject.trim(),
        description: state.description.trim(),
      });
    } finally {
      inFlightRef.current = false;
    }

    switch (outcome.kind) {
      case 'created':
        freshLocalResultRef.current = true;
        patch({ phase: 'created', result: outcome.data, errorMessage: null });
        break;
      case 'replayed':
        freshLocalResultRef.current = true;
        patch({ phase: 'replayed', result: outcome.data, errorMessage: null });
        break;
      case 'reconciliation_required':
        // Never an ordinary retryable error — the write may already have
        // happened. The Idempotency-Key is preserved (never regenerated)
        // and the form stays blocked; only explicit human verification
        // (outside this panel) resolves it.
        freshLocalResultRef.current = true;
        patch({ phase: 'reconciliation_required', problem: outcome.problem, errorMessage: null });
        break;
      case 'already_linked':
        // PRODUCT.6-M5: the active local ticket was already linked
        // BEFORE this create intent (a different Idempotency-Key created
        // it — another browser, a cleared localStorage, another agent).
        // This is deliberately NOT the same phase as 'replayed': no new
        // ticket was created by this call, and it is not this call's own
        // request being replayed.
        freshLocalResultRef.current = true;
        patch({ phase: 'already_linked', problem: outcome.problem, errorMessage: null });
        break;
      case 'definitive_failure':
        patch({
          phase: 'error',
          errorMessage: 'O chamado foi recusado pelo provedor. Revise os dados antes de tentar novamente.',
        });
        break;
      case 'unavailable':
        patch({ phase: 'error', errorMessage: 'Integração de chamados não configurada para este tenant.' });
        break;
      case 'conflict':
        patch({
          phase: 'error',
          errorMessage: 'Esta solicitação já foi usada com dados diferentes. Recarregue a página para começar uma nova.',
        });
        break;
      case 'invalid':
        patch({ phase: 'idle', errorMessage: outcome.message });
        break;
      case 'forbidden':
        patch({ phase: 'error', errorMessage: 'Sem permissão para criar chamados nesta conversa.' });
        break;
      case 'not_found':
        patch({ phase: 'error', errorMessage: 'Conversa ou chamado local não encontrado.' });
        break;
      case 'network_error':
        // Ambiguous: the write may or may not have reached the backend.
        // Same Idempotency-Key, same fields — a controlled manual retry of
        // the identical request is safe; an automatic one is not offered.
        patch({
          phase: 'idle',
          errorMessage: 'Falha de conexão. O chamado pode ou não ter sido criado — tente novamente com os mesmos dados.',
        });
        break;
    }
  };

  const noCompanies = !loadingCompanies && companies.length === 0;

  return (
    <div className="border-b border-border-subtle px-4">
      <section className="py-3" aria-labelledby="ticket-panel-heading">
        <h3 id="ticket-panel-heading" className={SECTION_TITLE}>
          Chamado
        </h3>

        {state.errorMessage && (
          <div
            role="alert"
            className="mb-2 rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-xs text-status-danger"
          >
            {state.errorMessage}
          </div>
        )}

        {readPhase === 'loading' ? (
          <div className="rounded-control border border-border-subtle bg-surface-muted p-3 text-xs text-text-secondary" aria-busy="true">
            Carregando chamado…
          </div>
        ) : localServerContradiction ? (
          // PRODUCT.6-O1F: server projection authority wins. This is a
          // fail-closed LOCAL/SERVER STATE INCONSISTENCY, not a server
          // "unlink" — CREATE is never offered automatically and no
          // provider contact happens here; only explicit human
          // verification (outside this panel) resolves it.
          <div className="rounded-control border border-status-warning-border bg-status-warning-soft p-3 text-xs text-status-warning">
            <p className="font-semibold">Verificação necessária</p>
            <p className="mt-1">
              O estado salvo neste navegador diverge do estado atual do servidor para esta conversa. Verificação
              necessária antes de qualquer nova ação.
            </p>
            {diagnosticExternalTicketId && (
              <p className="mt-1 font-mono text-[10px]">ID externo possível: {diagnosticExternalTicketId}</p>
            )}
            {diagnosticProvider && <p className="mt-1 font-mono text-[10px]">Provedor possível: {diagnosticProvider}</p>}
          </div>
        ) : finished && state.result ? (
          <div className="rounded-control border border-border-subtle bg-surface p-3">
            <p className="font-semibold text-text-primary">
              {state.phase === 'replayed' ? 'Chamado já criado' : 'Chamado criado'}
            </p>
            {state.phase === 'replayed' && (
              <p className="mt-1 text-text-secondary">Ticket já havia sido criado; resultado recuperado.</p>
            )}
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-2 gap-y-1 text-[11px]">
              <dt className="text-text-tertiary">ID externo</dt>
              <dd className="font-mono text-text-primary">{state.result.external_ticket_id}</dd>
              <dt className="text-text-tertiary">Provedor</dt>
              <dd className="text-text-primary">{state.result.provider}</dd>
              <dt className="text-text-tertiary">Sincronização</dt>
              <dd className="text-text-primary">{state.result.sync_status}</dd>
            </dl>
          </div>
        ) : alreadyLinked ? (
          <div className="rounded-control border border-border-subtle bg-surface p-3">
            <p className="font-semibold text-text-primary">Chamado já vinculado</p>
            <p className="mt-1 text-text-secondary">
              Esta conversa já possui um chamado externo vinculado — nenhum chamado novo foi criado.
            </p>
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-2 gap-y-1 text-[11px]">
              {state.problem?.external_ticket_id && (
                <>
                  <dt className="text-text-tertiary">ID externo</dt>
                  <dd className="font-mono text-text-primary">{state.problem.external_ticket_id}</dd>
                </>
              )}
              {state.problem?.provider && (
                <>
                  <dt className="text-text-tertiary">Provedor</dt>
                  <dd className="text-text-primary">{state.problem.provider}</dd>
                </>
              )}
            </dl>
          </div>
        ) : blocked ? (
          <div className="rounded-control border border-status-warning-border bg-status-warning-soft p-3 text-xs text-status-warning">
            <p className="font-semibold">Verificação necessária</p>
            <p className="mt-1">
              A criação pode ter sido processada. Verificação necessária antes de tentar novamente.
            </p>
            {state.problem?.external_ticket_id && (
              <p className="mt-1 font-mono text-[10px]">ID externo possível: {state.problem.external_ticket_id}</p>
            )}
          </div>
        ) : readPhase === 'linked' && linkedTicket ? (
          // PRODUCT.6-O1F: server truth (this session's own create/replay
          // result, if any, is rendered above via `finished`/`alreadyLinked`
          // — this branch is reached when the LINK was established by
          // something this panel instance never itself observed: another
          // browser/device/agent, or a prior session whose localStorage is
          // gone. Same visual shape, sourced from the GET response instead
          // of a create response.
          <>
          <div className="rounded-control border border-border-subtle bg-surface p-3">
            <div className="flex items-center justify-between gap-2">
              <p className="font-semibold text-text-primary">Chamado vinculado</p>
              <Button
                type="button"
                size="sm"
                isLoading={refreshState === 'pending'}
                disabled={refreshState === 'pending'}
                onClick={() => void refreshTicket()}
              >
                Atualizar
              </Button>
            </div>
            <dl className="mt-2 grid grid-cols-[auto_1fr] gap-x-2 gap-y-1 text-[11px]">
              <dt className="text-text-tertiary">ID externo</dt>
              <dd className="font-mono text-text-primary">{linkedTicket.external_ticket_id}</dd>
              <dt className="text-text-tertiary">Provedor</dt>
              <dd className="text-text-primary">{linkedTicket.provider}</dd>
              {linkedTicket.external_status_label && (
                <>
                  <dt className="text-text-tertiary">Status</dt>
                  <dd className="text-text-primary">{linkedTicket.external_status_label}</dd>
                </>
              )}
              <dt className="text-text-tertiary">Sincronização</dt>
              <dd className="text-text-primary">{linkedTicket.sync_status}</dd>
              {linkedTicket.last_synced_at && (
                <>
                  <dt className="text-text-tertiary">Última sincronização</dt>
                  <dd className="text-text-primary">{new Date(linkedTicket.last_synced_at).toLocaleString('pt-BR')}</dd>
                </>
              )}
            </dl>

            {/* PRODUCT.6-O2BF: status mutation controls — only when the
                ticket carries a real external identity (provider +
                external_ticket_id), never for the projection-read
                loading/unlinked/404/inconsistent branches (those never
                reach this JSX branch at all). */}
            {hasLinkedIdentity && (
              <div className="mt-3 border-t border-border-subtle pt-3">
                <p className={SECTION_TITLE}>Status externo</p>

                {canVerifyPendingIntent ? (
                  <div className="rounded-control border border-status-warning-border bg-status-warning-soft p-2 text-[11px] text-status-warning">
                    <p className="font-semibold">
                      {mutationPhase === 'network_ambiguous'
                        ? 'Não foi possível confirmar a alteração.'
                        : 'Verificação necessária'}
                    </p>
                    <Button
                      type="button"
                      size="sm"
                      className="mt-2"
                      isLoading={mutationPhase === 'pending'}
                      disabled={mutationPhase === 'pending'}
                      onClick={() => void verifyPendingIntent()}
                    >
                      Verificar alteração
                    </Button>
                  </div>
                ) : hasKnownProviderOptions ? (
                  <>
                    <div className="flex items-center gap-2">
                      <select
                        id="ticket-status-select"
                        className={FIELD}
                        aria-label="Status externo"
                        value={selectedTarget}
                        onChange={(e) => {
                          setSelectedTarget(e.target.value);
                          setMutationNote(null);
                        }}
                        disabled={statusControlsLocked}
                      >
                        <option value="">Selecionar status</option>
                        {providerOptions!.map((opt) => (
                          <option key={opt.code} value={opt.code}>
                            {opt.label} ({opt.code})
                          </option>
                        ))}
                      </select>
                      <Button
                        type="button"
                        size="sm"
                        isLoading={mutationPhase === 'pending'}
                        disabled={alterarStatusDisabled}
                        onClick={() => setConfirmOpen(true)}
                      >
                        Alterar status
                      </Button>
                    </div>
                    {mutationNote && <p className="mt-2 text-[11px] text-text-secondary">{mutationNote}</p>}
                    {mutationPhase === 'unprocessable' && (
                      <p className="mt-2 text-[11px] text-status-danger">
                        Não foi possível confirmar esta alteração de status. Revise o status atual e tente novamente
                        com uma nova seleção.
                      </p>
                    )}
                    {mutationPhase === 'forbidden' && (
                      <p className="mt-2 text-[11px] text-status-danger">
                        Sem permissão para alterar o status deste chamado.
                      </p>
                    )}
                    {mutationPhase === 'unavailable' && (
                      <p className="mt-2 text-[11px] text-status-danger">
                        Não foi possível confirmar a alteração agora.
                      </p>
                    )}
                    {mutationPhase === 'not_found' && (
                      <p className="mt-2 text-[11px] text-status-warning">
                        Verificação necessária antes de tentar novamente.
                      </p>
                    )}
                  </>
                ) : null}
              </div>
            )}

            {/* PRODUCT.6-O1RF: refresh is a read-only provider operation —
                its own errors never blank this card or fall back to
                linked=false/CREATE. The last known projection above remains
                visible and useful regardless of the outcome below. */}
            {refreshState === 'reconciliation' && (
              <div className="mt-2 rounded-control border border-status-warning-border bg-status-warning-soft p-2 text-[11px] text-status-warning">
                <p className="font-semibold">Verificação necessária</p>
                <p className="mt-1">
                  Chamado vinculado, mas não foi possível confirmá-lo no provedor agora. O vínculo existente foi
                  preservado.
                </p>
              </div>
            )}
            {refreshState === 'unavailable' && (
              <p className="mt-2 text-[11px] text-status-danger">Não foi possível atualizar o chamado agora.</p>
            )}
            {refreshState === 'forbidden' && (
              <p className="mt-2 text-[11px] text-status-danger">Sem permissão para atualizar o chamado desta conversa.</p>
            )}
            {refreshState === 'network_error' && (
              <p className="mt-2 text-[11px] text-status-danger">Falha de conexão ao atualizar o chamado.</p>
            )}
          </div>
          <ConfirmDialog
            open={confirmOpen}
            title="Alterar status do chamado"
            message={`Alterar status do chamado externo de "${currentStatusLabel}" para "${targetStatusLabel}"?`}
            confirmLabel="Confirmar alteração"
            isPending={mutationPhase === 'pending'}
            onCancel={() => setConfirmOpen(false)}
            onConfirm={() => void confirmStatusMutation()}
          />
          </>
        ) : readPhase === 'not_found' ? (
          <div className="rounded-control border border-border-subtle bg-surface-muted p-3 text-xs text-text-secondary">
            Não há chamado ativo nesta conversa para vincular ao ERP.
          </div>
        ) : readPhase === 'inconsistent' ? (
          <div className="rounded-control border border-status-warning-border bg-status-warning-soft p-3 text-xs text-status-warning">
            <p className="font-semibold">Verificação necessária</p>
            <p className="mt-1">
              O vínculo deste chamado com o ERP está inconsistente. Verificação necessária antes de qualquer nova
              ação.
            </p>
          </div>
        ) : readPhase === 'forbidden' ? (
          <div className="rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-xs text-status-danger">
            Sem permissão para ver o chamado desta conversa.
          </div>
        ) : readPhase === 'unavailable' ? (
          <div className="rounded-control border border-border-subtle bg-surface-muted p-3 text-xs text-text-secondary">
            Integração de chamados não configurada para este tenant.
          </div>
        ) : readPhase === 'network_error' ? (
          <div className="rounded-control border border-status-danger-border bg-status-danger-soft p-3 text-xs text-status-danger">
            Falha ao carregar o chamado desta conversa. Tente novamente mais tarde.
          </div>
        ) : (
          <form
            className="grid gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void submitTicket();
            }}
          >
            <select
              className={FIELD}
              aria-label="Empresa"
              value={state.selectedCompanyId}
              onChange={(e) => patch({ selectedCompanyId: e.target.value })}
              disabled={submitting || loadingCompanies || companies.length === 0}
            >
              <option value="">Selecionar empresa</option>
              {companies.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.name}
                  {c.cnpj ? ` (${c.cnpj})` : ''}
                </option>
              ))}
            </select>
            {noCompanies && <p className="m-0 text-[10px] text-text-tertiary">Nenhuma empresa disponível</p>}
            <input
              className={FIELD}
              value={state.subject}
              onChange={(e) => patch({ subject: e.target.value })}
              placeholder="Assunto do chamado..."
              aria-label="Assunto do chamado"
              disabled={submitting}
            />
            <textarea
              className={FIELD}
              value={state.description}
              onChange={(e) => patch({ description: e.target.value })}
              placeholder="Descrição do chamado..."
              aria-label="Descrição do chamado"
              disabled={submitting}
              rows={2}
            />
            <Button
              type="submit"
              size="sm"
              isLoading={submitting}
              disabled={submitting || !state.selectedCompanyId || !state.subject.trim() || !state.description.trim()}
            >
              Abrir chamado
            </Button>
          </form>
        )}
      </section>

      {crmContactId && (
        <section className="border-t border-border-subtle py-3" aria-labelledby="crm-activity-heading">
          <h3 id="crm-activity-heading" className={SECTION_TITLE}>
            Atividade CRM
          </h3>
          {/*
            PRODUCT.7B1B (security correction): activity creation is
            TEMPORARILY CONTAINED. A security review found that knowing a
            company exists in the tenant's CRM directory does not prove it
            is the company associated with this conversation — no
            authoritative link exists yet, so the create form (which
            offered a company picker to the browser) was removed rather
            than kept while the server silently refuses every submission.
            Restored once PRODUCT.7B2 establishes that linkage.
          */}
          <div className="rounded-control border border-border-subtle bg-surface-muted p-3 text-xs text-text-secondary">
            Criação de atividade indisponível até que o vínculo com a empresa do cliente seja estabelecido.
          </div>
        </section>
      )}
    </div>
  );
}
