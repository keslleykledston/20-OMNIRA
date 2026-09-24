import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import { TicketPanel } from '../components/TicketPanel';
import { renderAt, setSession, TENANT } from './testUtils';

// PRODUCT.6-N: real external ticket creation through
// POST /api/v1/tenants/{tenant_id}/conversations/{conversation_id}/ticket
// (PRODUCT.6-M). All HTTP is mocked — no live K3G call is ever made from
// these tests.
vi.mock('axios');

const CONV = 'c-1';
const COMPANY = { id: 'd38e7970-635d-490b-a119-749ee6f1fe23', name: 'ACME_TESTE', cnpj: '79.191.760/0001-83' };

// Default axios.get mock: real company list (PRODUCT.6-N) PLUS the real
// O1 conversation ticket GET (PRODUCT.6-O1F) resolving to "unlinked" (an
// active local ticket exists, no external link yet) — the state every
// pre-existing create-flow test below assumes. Tests that specifically
// exercise a different read outcome override axios.get themselves.
function mockCompanies(items: unknown[] = [COMPANY]) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/integrations/companies')) return { data: { items } };
    if (url.endsWith('/ticket')) return { data: { local_ticket_id: 'lt-1', linked: false } };
    return Promise.reject({ response: { status: 404 } });
  });
}

function mockRead(outcome: { status: number; data?: unknown } | 'network_error') {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/integrations/companies')) return { data: { items: [COMPANY] } };
    if (url.endsWith('/ticket')) {
      if (outcome === 'network_error') return Promise.reject({ message: 'Network Error' });
      if (outcome.status === 200) return { data: outcome.data };
      return Promise.reject({ response: { status: outcome.status, data: outcome.data } });
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

// PRODUCT.6-O1RF: answers axios.post's REFRESH route
// (.../ticket/refresh) by outcome, without touching the CREATE route
// (.../ticket) — each is a separate mockImplementation set per-test, so
// this never interferes with the create-flow tests above.
function mockRefresh(outcome: { status: number; data?: unknown } | 'network_error') {
  vi.mocked(axios.post).mockImplementation(async (url: string) => {
    if (url.endsWith('/ticket/refresh')) {
      if (outcome === 'network_error') return Promise.reject({ message: 'Network Error' });
      if (outcome.status === 200) return { data: outcome.data };
      return Promise.reject({ response: { status: outcome.status, data: outcome.data } });
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

const LINKED = { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', external_status: '1', external_status_label: 'Novo', sync_status: 'synced' };

async function fillForm(subject = 'Assunto', description = 'Descricao') {
  await screen.findByLabelText('Empresa');
  fireEvent.change(screen.getByLabelText('Empresa'), { target: { value: COMPANY.id } });
  fireEvent.change(screen.getByLabelText('Assunto do chamado'), { target: { value: subject } });
  fireEvent.change(screen.getByLabelText('Descrição do chamado'), { target: { value: description } });
}

beforeEach(() => {
  vi.clearAllMocks();
  localStorage.clear();
  setSession();
  mockCompanies();
});

describe('TicketPanel — create form', () => {
  // A
  it('renders provider-backed companies from the real company list', async () => {
    mockCompanies([COMPANY, { id: 'other-id', name: 'Other Co' }]);
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText(`${COMPANY.name} (${COMPANY.cnpj})`);
    expect(screen.getByText('Other Co')).toBeInTheDocument();
  });

  // B
  it('offers no free-text input for the company — only the provider-backed select', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    expect(screen.getByLabelText('Empresa').tagName).toBe('SELECT');
    expect(screen.queryByLabelText(/company.*id/i)).not.toBeInTheDocument();
  });

  // C
  it('requires a subject before the submit button is enabled', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    fireEvent.change(screen.getByLabelText('Empresa'), { target: { value: COMPANY.id } });
    fireEvent.change(screen.getByLabelText('Descrição do chamado'), { target: { value: 'x' } });
    expect(screen.getByRole('button', { name: 'Abrir chamado' })).toBeDisabled();
  });

  // D
  it('requires a description before the submit button is enabled', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    fireEvent.change(screen.getByLabelText('Empresa'), { target: { value: COMPANY.id } });
    fireEvent.change(screen.getByLabelText('Assunto do chamado'), { target: { value: 'x' } });
    expect(screen.getByRole('button', { name: 'Abrir chamado' })).toBeDisabled();
  });

  // E
  it('gives a fresh create intent exactly one Idempotency-Key, sent on submit', async () => {
    vi.mocked(axios.post).mockResolvedValue({
      status: 201,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    const [url, , config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(`/api/v1/tenants/${TENANT}/conversations/${CONV}/ticket`);
    const key = config.headers['Idempotency-Key'];
    expect(key).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}');
    expect(persisted.idempotencyKey).toBe(key);
  });

  // F
  it('a double click produces exactly one HTTP request', async () => {
    let resolvePost: (v: unknown) => void = () => {};
    vi.mocked(axios.post).mockImplementation(() => new Promise((resolve) => (resolvePost = resolve)));
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    const button = screen.getByRole('button', { name: 'Abrir chamado' });
    fireEvent.click(button);
    fireEvent.click(button);
    resolvePost({
      status: 201,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
    });
    await screen.findByText('Chamado criado');
    expect(axios.post).toHaveBeenCalledTimes(1);
  });

  // G
  it('a controlled retry after a network failure reuses the same Idempotency-Key', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    vi.mocked(axios.post).mockRejectedValueOnce({ message: 'Network Error' });
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText(/Falha de conexão/);
    const keyAfterFailure = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;

    vi.mocked(axios.post).mockResolvedValueOnce({
      status: 200,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: true },
    });
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(2));
    const secondKey = (vi.mocked(axios.post).mock.calls[1] as any[])[2].headers['Idempotency-Key'];
    expect(secondKey).toBe(keyAfterFailure);
  });

  // H
  it('HTTP 201 renders the created state', async () => {
    vi.mocked(axios.post).mockResolvedValue({
      status: 201,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado criado');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.queryByText(/já havia sido criado/)).not.toBeInTheDocument();
  });

  // I
  it('HTTP 200 + replayed renders replay success, never a duplicate-create message', async () => {
    vi.mocked(axios.post).mockResolvedValue({
      status: 200,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: true },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado já criado');
    expect(screen.getByText(/já havia sido criado; resultado recuperado/)).toBeInTheDocument();
  });

  // J
  it('409 TICKET_RECONCILIATION_REQUIRED blocks retry and shows an explicit safe state', async () => {
    vi.mocked(axios.post).mockRejectedValue({
      response: {
        status: 409,
        data: { error: 'reconciliation required', code: 'TICKET_RECONCILIATION_REQUIRED', attempt_id: 'a-1', attempt_state: 'outcome_unknown', external_ticket_id: '28182' },
      },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Verificação necessária');
    expect(screen.getByText(/pode ter sido processada/)).toBeInTheDocument();
    expect(screen.getByText(/28182/)).toBeInTheDocument();
    // The form is gone; there is no way to blindly re-submit.
    expect(screen.queryByRole('button', { name: 'Abrir chamado' })).not.toBeInTheDocument();
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}');
    expect(persisted.phase).toBe('reconciliation_required');
  });

  // P
  it('409 TICKET_ALREADY_LINKED shows the already-linked state, never a replay or a new-ticket message', async () => {
    vi.mocked(axios.post).mockRejectedValue({
      response: {
        status: 409,
        data: { error: 'already linked', code: 'TICKET_ALREADY_LINKED', local_ticket_id: 'lt-1', provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' },
      },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado já vinculado');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.getByText('k3g')).toBeInTheDocument();
    expect(screen.queryByText('Chamado criado')).not.toBeInTheDocument();
    expect(screen.queryByText(/já havia sido criado; resultado recuperado/)).not.toBeInTheDocument();
    expect(screen.queryByText('Verificação necessária')).not.toBeInTheDocument();
    // No way to blindly re-submit or automatically retry.
    expect(screen.queryByRole('button', { name: 'Abrir chamado' })).not.toBeInTheDocument();
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}');
    expect(persisted.phase).toBe('already_linked');
  });

  // Q
  it('already_linked state persists across remount when the server GET agrees (linked=true)', async () => {
    vi.mocked(axios.post).mockRejectedValue({
      response: {
        status: 409,
        data: { error: 'already linked', code: 'TICKET_ALREADY_LINKED', external_ticket_id: '28182', provider: 'k3g' },
      },
    });
    const { unmount } = renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado já vinculado');
    unmount();

    // The remount's own GET must independently confirm the link — the
    // fresh in-session 409 that produced 'already_linked' only covers the
    // instance that received it (PRODUCT.6-O1F section 3); a NEW mount is
    // authoritative only via its own server round trip.
    mockRead({
      status: 200,
      data: { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    // GET agrees with the persisted local state (linked=true) — no
    // contradiction, so the panel may keep rendering the already-linked
    // card it already had (still not the CREATE form).
    await screen.findByText('Chamado já vinculado');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
    expect(axios.post).toHaveBeenCalledTimes(1);
  });

  // K
  it('422 conflict does not silently regenerate the Idempotency-Key', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    const keyBefore = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422, data: 'Idempotency-Key was already used with a different request' } });
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText(/já foi usada com dados diferentes/);
    const keyAfter = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    expect(keyAfter).toBe(keyBefore);
  });

  // L
  it('503 shows the integration-unavailable state', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 503, data: 'ticketing integration not configured for this tenant' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Integração de chamados não configurada para este tenant.');
  });

  // M
  it('a network failure preserves the same Idempotency-Key for a controlled retry', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    const keyBefore = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    vi.mocked(axios.post).mockRejectedValue({ message: 'Network Error' });
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText(/Falha de conexão/);
    const keyAfter = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    expect(keyAfter).toBe(keyBefore);
    expect(screen.getByRole('button', { name: 'Abrir chamado' })).not.toBeDisabled();
  });

  // N
  it('never sends tenant, actor, provider or K3G-specific fields in the body', async () => {
    vi.mocked(axios.post).mockResolvedValue({
      status: 201,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm('Assunto X', 'Descricao Y');
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    const [, body] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(Object.keys(body).sort()).toEqual(['description', 'selected_customer_external_id', 'subject']);
    expect(body.selected_customer_external_id).toBe(COMPANY.id);
  });

  // O
  it('never exposes GET/UPDATE/CLOSE lifecycle controls', async () => {
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    expect(screen.queryByRole('button', { name: /Trabalhando/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Resolvido/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Fechar/i })).not.toBeInTheDocument();
  });
});

describe('TicketPanel — PRODUCT.6-O1F conversation ticket read', () => {
  // A
  it('does not render the create form before the read GET resolves', async () => {
    let resolveGet: (v: unknown) => void = () => {};
    vi.mocked(axios.get).mockImplementation((url: string) => {
      if (url.endsWith('/integrations/companies')) return Promise.resolve({ data: { items: [COMPANY] } });
      if (url.endsWith('/ticket')) return new Promise((resolve) => (resolveGet = resolve));
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    expect(screen.getByText('Carregando chamado…')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
    resolveGet({ data: { local_ticket_id: 'lt-1', linked: false } });
    await screen.findByLabelText('Empresa');
  });

  // B
  it('linked=false shows the create form', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
  });

  // C, D
  it('linked=true shows the linked-ticket card with O1 fields and hides the create form', async () => {
    mockRead({
      status: 200,
      data: {
        local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182',
        external_status: '1', external_status_label: 'Novo', sync_status: 'synced', last_synced_at: '2026-09-24T06:15:45Z',
      },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.getByText('k3g')).toBeInTheDocument();
    expect(screen.getByText('Novo')).toBeInTheDocument();
    expect(screen.getByText('synced')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // E
  it('404 shows the no-active-ticket state and hides the create form', async () => {
    mockRead({ status: 404 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Não há chamado ativo nesta conversa para vincular ao ERP.');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // F
  it('inconsistent linkage (409) shows a fail-closed state and hides the create form', async () => {
    mockRead({ status: 409, data: { error: 'inconsistent', code: 'TICKET_INCONSISTENT_LINK' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Verificação necessária');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // G
  it('a GET network failure hides the create form (fails closed)', async () => {
    mockRead('network_error');
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Falha ao carregar o chamado desta conversa. Tente novamente mais tarde.');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // H
  it('stale localStorage idle + GET linked=true: server wins, create hidden', async () => {
    localStorage.setItem(`omnira.ticket-create.${CONV}`, JSON.stringify({ idempotencyKey: 'k', phase: 'idle' }));
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // I — documented decision: a terminal LOCAL create phase (this browser's
  // own prior successful create/replay/already_linked/reconciliation
  // result) always renders over a contradicting GET, because the backend
  // has no unlink capability — this combination cannot arise from
  // legitimate traffic. GET remains authoritative only for the FIRST
  // decision in a browser that has not itself completed a create action.
  it('stale localStorage says created but GET says linked=false: fail-closed inconsistency, not a blind local win', async () => {
    localStorage.setItem(
      `omnira.ticket-create.${CONV}`,
      JSON.stringify({
        idempotencyKey: 'k', phase: 'created',
        result: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
      }),
    );
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);

    // The stale "Chamado criado" card must NOT be rendered as authoritative...
    await screen.findByText('Verificação necessária');
    expect(screen.queryByText('Chamado criado')).not.toBeInTheDocument();
    // ...and CREATE must not be blindly offered either (this is a
    // disagreement to resolve, not evidence the ticket was never created).
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
    // The stale local evidence (external_ticket_id) is preserved for a
    // human to use during verification.
    expect(screen.getByText(/28182/)).toBeInTheDocument();
    // No automatic write of any kind happens on this disagreement.
    expect(axios.post).not.toHaveBeenCalled();
  });

  // J
  it('a successful CREATE updates the panel to the created state immediately, without a second GET', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    vi.mocked(axios.post).mockResolvedValue({
      status: 201,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: false },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    const getCallsBefore = vi.mocked(axios.get).mock.calls.filter((c) => (c[0] as string).endsWith('/ticket')).length;
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado criado');
    const getCallsAfter = vi.mocked(axios.get).mock.calls.filter((c) => (c[0] as string).endsWith('/ticket')).length;
    expect(getCallsAfter).toBe(getCallsBefore);
  });

  // K
  it('remount after a successful create: GET linked=true alone suppresses CREATE, independent of localStorage', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' } });
    localStorage.clear(); // simulate a fresh browser/cleared storage
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // L
  it('preserves 200 idempotent replay behavior', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    vi.mocked(axios.post).mockResolvedValue({
      status: 200,
      data: { local_ticket_id: 'lt-1', external_ticket_id: '28182', provider: 'k3g', sync_status: 'synced', replayed: true },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado já criado');
  });

  // M
  it('preserves 409 TICKET_RECONCILIATION_REQUIRED behavior', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    vi.mocked(axios.post).mockRejectedValue({
      response: { status: 409, data: { error: 'reconciliation required', code: 'TICKET_RECONCILIATION_REQUIRED' } },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Verificação necessária');
  });

  // N
  it('preserves the pending Idempotency-Key across a network failure/retry', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    const keyBefore = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    vi.mocked(axios.post).mockRejectedValue({ message: 'Network Error' });
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText(/Falha de conexão/);
    const keyAfter = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}').idempotencyKey;
    expect(keyAfter).toBe(keyBefore);
  });

  // O — mount/GET alone never triggers a refresh call (PRODUCT.6-O1RF:
  // refresh exists but is strictly user-initiated — see the dedicated
  // describe block below for the click-triggered behavior).
  it('never calls a provider refresh endpoint', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    const urls = vi.mocked(axios.get).mock.calls.map((c) => c[0] as string);
    expect(urls.some((u) => u.includes('refresh'))).toBe(false);
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled();
  });

  // P
  it('UPDATE/CLOSE controls remain absent when a ticket is linked', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: true, provider: 'k3g', external_ticket_id: '28182', sync_status: 'synced' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.queryByRole('button', { name: /Trabalhando/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Resolvido/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Fechar/i })).not.toBeInTheDocument();
  });
});

describe('TicketPanel — PRODUCT.6-O1RF explicit provider refresh', () => {
  // A
  it('linked=true: refresh button visible', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    expect(await screen.findByRole('button', { name: 'Atualizar' })).toBeInTheDocument();
  });

  // B
  it('linked=false: refresh button absent', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    expect(screen.queryByRole('button', { name: 'Atualizar' })).not.toBeInTheDocument();
  });

  // C, U
  it('initial mount: conversation GET happens, refresh POST does not happen automatically', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(vi.mocked(axios.get).mock.calls.some((c) => (c[0] as string).endsWith('/ticket'))).toBe(true);
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled();
  });

  // D, E
  it('refresh click: exactly one POST to the canonical refresh route, with no request body', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1));
    const [url, body] = vi.mocked(axios.post).mock.calls[0];
    expect((url as string).endsWith(`/tenants/${TENANT}/conversations/${CONV}/ticket/refresh`)).toBe(true);
    expect(body).toBeUndefined();
  });

  // F
  it('double click: exactly one request while pending', async () => {
    mockRead({ status: 200, data: LINKED });
    let resolvePost: (v: unknown) => void = () => {};
    vi.mocked(axios.post).mockImplementation(
      (url: string) =>
        new Promise((resolve, reject) => {
          if (!(url as string).endsWith('/ticket/refresh')) {
            reject({ response: { status: 404 } });
            return;
          }
          resolvePost = resolve;
        }),
    );
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    const button = screen.getByRole('button', { name: 'Atualizar' });
    fireEvent.click(button);
    fireEvent.click(button);
    resolvePost({ data: LINKED });
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1));
  });

  // G
  it('pending refresh: existing linked data remains visible, button disabled/loading', async () => {
    mockRead({ status: 200, data: LINKED });
    let resolvePost: (v: unknown) => void = () => {};
    vi.mocked(axios.post).mockImplementation(
      () =>
        new Promise((resolve) => {
          resolvePost = resolve;
        }),
    );
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    const button = screen.getByRole('button', { name: 'Atualizar' });
    fireEvent.click(button);
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(button).toBeDisabled();
    resolvePost({ data: LINKED });
    await waitFor(() => expect(button).not.toBeDisabled());
  });

  // H, I, J
  it('successful refresh updates external_status/label/last_synced_at', async () => {
    mockRead({ status: 200, data: LINKED });
    const refreshed = { ...LINKED, external_status: '2', external_status_label: 'Em atendimento', last_synced_at: '2026-01-01T12:00:00Z' };
    mockRefresh({ status: 200, data: refreshed });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Em atendimento');
    expect(screen.getByText(new Date('2026-01-01T12:00:00Z').toLocaleString('pt-BR'))).toBeInTheDocument();
  });

  // K
  it('successful refresh: external identity (provider/external_ticket_id) unchanged', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: { ...LINKED, external_status: '2', external_status_label: 'Em atendimento' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Em atendimento');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.getByText('k3g')).toBeInTheDocument();
  });

  // defensive frontend guard on top of the backend's own identity guard
  it('a response that unexpectedly changes external identity is treated as reconciliation, never silently applied', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: { ...LINKED, external_ticket_id: '99999' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Verificação necessária');
    // the ORIGINAL identity remains displayed — never silently swapped.
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.queryByText('99999')).not.toBeInTheDocument();
  });

  // L
  it('503: existing linked data remains visible, non-destructive error shown, no CREATE form', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 503 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Não foi possível atualizar o chamado agora.');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // M
  it('409 reconciliation: existing link remains visible, verification-required state shown, no CREATE form', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 409 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Verificação necessária');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // N — the backend has no machine-readable distinction for provider
  // NOT_FOUND/NOT_MIGRATED vs. other reconciliation-required refresh
  // outcomes (all plain-text 409s); the safe UI treatment is identical.
  it('provider NOT_FOUND/not-migrated-style 409: external link not cleared, no CREATE offered', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 409, data: 'external ticket not found' });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Verificação necessária');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // O
  it('network error: linked state preserved, manual refresh remains possible, no automatic retry', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh('network_error');
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Falha de conexão ao atualizar o chamado.');
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1); // no automatic retry
    expect(screen.getByRole('button', { name: 'Atualizar' })).not.toBeDisabled(); // manual retry remains possible
  });

  // P
  it('unauthorized refresh: permission state shown, no create fallback', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 403 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Sem permissão para atualizar o chamado desta conversa.');
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
  });

  // Q
  it('conversation switch while a refresh request is pending: a late response for the old conversation does not overwrite the new one', async () => {
    mockRead({ status: 200, data: LINKED });
    let resolveOldRefresh: (v: unknown) => void = () => {};
    vi.mocked(axios.post).mockImplementation(
      (url: string) =>
        new Promise((resolve, reject) => {
          if (!(url as string).endsWith('/ticket/refresh')) {
            reject({ response: { status: 404 } });
            return;
          }
          resolveOldRefresh = resolve;
        }),
    );

    function Switcher() {
      const [conv, setConv] = useState(CONV);
      return (
        <div>
          <button onClick={() => setConv('c-2')}>switch conversation</button>
          <TicketPanel conversationId={conv} />
        </div>
      );
    }
    renderAt(<Switcher />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));

    fireEvent.click(screen.getByRole('button', { name: 'switch conversation' }));
    await screen.findByText('Chamado vinculado'); // conversation c-2's own GET resolves

    // The stale refresh (from conversation CONV) now resolves with data
    // that must never be applied to the panel now showing conversation c-2.
    resolveOldRefresh({ data: { ...LINKED, external_ticket_id: 'STALE-FROM-OLD-CONVERSATION' } });
    await waitFor(() => {});
    expect(screen.queryByText('STALE-FROM-OLD-CONVERSATION')).not.toBeInTheDocument();
  });

  // R
  it('localStorage: refresh result never becomes linkage authority / is not persisted there', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: { ...LINKED, external_status: '2', external_status_label: 'Em atendimento' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Em atendimento');
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-create.${CONV}`) || '{}');
    expect(persisted.phase).not.toBe('linked');
    expect(JSON.stringify(persisted)).not.toContain('Em atendimento');
  });

  // S — refresh work must not regress the linked=false CREATE flow.
  it('CREATE regression: linked=false create flow still works end to end', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (url.endsWith('/ticket')) {
        return {
          status: 201,
          data: { local_ticket_id: 'lt-1', external_ticket_id: '28180', provider: 'k3g', sync_status: 'synced', replayed: false },
        };
      }
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await fillForm();
    fireEvent.click(screen.getByRole('button', { name: 'Abrir chamado' }));
    await screen.findByText('Chamado criado');
  });

  // T
  it('UPDATE/CLOSE controls remain absent after a successful refresh', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1));
    expect(screen.queryByRole('button', { name: /Trabalhando/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Resolvido/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Fechar/i })).not.toBeInTheDocument();
  });
});
