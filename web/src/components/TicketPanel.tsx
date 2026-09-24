import React, { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { API_BASE } from '../lib/config';
import { authHeaders, getTenantId } from '../lib/session';
import {
  createExternalTicket,
  type ExternalTicketCreateResponse,
  type ExternalTicketProblem,
} from '../lib/tickets';
import { Button } from './primitives';

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

export function TicketPanel({ conversationId, crmContactId }: TicketPanelProps) {
  const tenantId = getTenantId();

  const [state, setState] = useState<PersistedState>(() => loadState(conversationId));

  // A genuinely new create intent starts only when the panel switches to a
  // different conversation — never merely because of a re-render, a
  // network retry, or a page reload of the SAME conversation.
  useEffect(() => {
    setState(loadState(conversationId));
  }, [conversationId]);

  useEffect(() => {
    saveState(conversationId, state);
  }, [conversationId, state]);

  const patch = (partial: Partial<PersistedState>) => setState((prev) => ({ ...prev, ...partial }));

  // R5.2: CRM Activity + the trusted, provider-backed company source
  // (PRODUCT.6-K0/6-K2: browser-provided IDs are never trusted directly —
  // this list is the ONLY source the create form offers, so an arbitrary
  // free-text external ID cannot be submitted through this UI).
  const [companies, setCompanies] = useState<Company[]>([]);
  const [activitySubject, setActivitySubject] = useState('');
  const [activityCompanyId, setActivityCompanyId] = useState('');
  const [loadingCompanies, setLoadingCompanies] = useState(false);
  const [activityError, setActivityError] = useState<string | null>(null);
  const [activityLoading, setActivityLoading] = useState(false);

  useEffect(() => {
    const loadCompanies = async () => {
      setLoadingCompanies(true);
      try {
        const res = await axios.get(`${API_BASE}/integrations/companies`, { headers: authHeaders() });
        const items: Company[] = res.data.items || [];
        setCompanies(items);
        if (items.length > 0) setActivityCompanyId((prev) => prev || items[0].id);
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
        patch({ phase: 'created', result: outcome.data, errorMessage: null });
        break;
      case 'replayed':
        patch({ phase: 'replayed', result: outcome.data, errorMessage: null });
        break;
      case 'reconciliation_required':
        // Never an ordinary retryable error — the write may already have
        // happened. The Idempotency-Key is preserved (never regenerated)
        // and the form stays blocked; only explicit human verification
        // (outside this panel) resolves it.
        patch({ phase: 'reconciliation_required', problem: outcome.problem, errorMessage: null });
        break;
      case 'already_linked':
        // PRODUCT.6-M5: the active local ticket was already linked
        // BEFORE this create intent (a different Idempotency-Key created
        // it — another browser, a cleared localStorage, another agent).
        // This is deliberately NOT the same phase as 'replayed': no new
        // ticket was created by this call, and it is not this call's own
        // request being replayed.
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

  // R5.2: Create activity in CRM (unrelated to external ticket creation;
  // PRODUCT.6-K0 already flagged this flow's company_id as an unfixed
  // trust gap — left unchanged in this slice).
  const createActivity = async () => {
    if (!activitySubject.trim() || !activityCompanyId || !crmContactId) return;
    setActivityLoading(true);
    setActivityError(null);
    try {
      await axios.post(
        `${API_BASE}/tenants/${tenantId}/conversations/${conversationId}/crm/activity`,
        { subject: activitySubject, company_id: activityCompanyId, contact_id: crmContactId },
        { headers: authHeaders() },
      );
      setActivitySubject('');
    } catch (err: any) {
      setActivityError(err.response?.data?.message || 'Erro ao criar atividade no CRM');
    } finally {
      setActivityLoading(false);
    }
  };

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

        {finished && state.result ? (
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
          {activityError && (
            <div
              role="alert"
              className="mb-2 rounded-control border border-status-danger-border bg-status-danger-soft px-3 py-2 text-xs text-status-danger"
            >
              {activityError}
            </div>
          )}
          <form
            className="grid gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              void createActivity();
            }}
          >
            <select
              className={FIELD}
              aria-label="Empresa da atividade"
              value={activityCompanyId}
              onChange={(e) => setActivityCompanyId(e.target.value)}
              disabled={activityLoading || loadingCompanies || companies.length === 0}
            >
              {companies.length === 0 && <option value="">Selecionar empresa</option>}
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
              value={activitySubject}
              onChange={(e) => setActivitySubject(e.target.value)}
              placeholder="Descrição da atividade..."
              aria-label="Descrição da atividade"
              disabled={activityLoading}
            />
            <Button type="submit" size="sm" isLoading={activityLoading} disabled={activityLoading || !activitySubject.trim() || !activityCompanyId}>
              Criar Atividade
            </Button>
          </form>
        </section>
      )}
    </div>
  );
}
