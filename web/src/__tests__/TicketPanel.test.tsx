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
    if (url.endsWith('/crm/companies')) return { data: { items } };
    if (url.endsWith('/ticket')) return { data: { local_ticket_id: 'lt-1', linked: false } };
    return Promise.reject({ response: { status: 404 } });
  });
}

function mockRead(outcome: { status: number; data?: unknown } | 'network_error') {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/crm/companies')) return { data: { items: [COMPANY] } };
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

// PRODUCT.6-O2BF: answers axios.post's STATUS route (.../ticket/status) by
// outcome, without touching the CREATE (.../ticket) or REFRESH
// (.../ticket/refresh) routes — mirrors mockRefresh's convention exactly.
function mockStatusMutation(outcome: { status: number; data?: unknown } | 'network_error') {
  vi.mocked(axios.post).mockImplementation(async (url: string) => {
    if (url.endsWith('/ticket/status')) {
      if (outcome === 'network_error') return Promise.reject({ message: 'Network Error' });
      if (outcome.status >= 200 && outcome.status < 300) return { data: outcome.data };
      return Promise.reject({ response: { status: outcome.status, data: outcome.data } });
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

function stalePersistedStatusIntent(
  overrides: Partial<{
    tenantId: string;
    conversationId: string;
    localTicketId: string;
    provider: string;
    externalTicketId: string;
  }> = {},
) {
  return {
    tenantId: TENANT,
    conversationId: CONV,
    localTicketId: 'lt-1',
    provider: 'k3g',
    externalTicketId: '28182',
    targetStatus: '5',
    idempotencyKey: 'stale-key-00000001',
    ...overrides,
  };
}

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
      if (url.endsWith('/crm/companies')) return Promise.resolve({ data: { items: [COMPANY] } });
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

describe('TicketPanel — PRODUCT.6-O2BF status mutation', () => {
  const statusPostCalls = () =>
    vi.mocked(axios.post).mock.calls.filter((c) => (c[0] as string).endsWith('/ticket/status'));

  async function openConfirmFor(target: string) {
    fireEvent.change(screen.getByLabelText('Status externo'), { target: { value: target } });
    fireEvent.click(screen.getByRole('button', { name: 'Alterar status' }));
    await screen.findByRole('dialog');
  }

  // A
  it('linked=false: status controls hidden', async () => {
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    expect(screen.queryByLabelText('Status externo')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Alterar status' })).not.toBeInTheDocument();
  });

  // B
  it('linked=true with a known provider (k3g): status controls visible', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.getByLabelText('Status externo')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Alterar status' })).toBeInTheDocument();
  });

  // C
  it('unknown provider: read-only external status, no mutation selector', async () => {
    mockRead({ status: 200, data: { ...LINKED, provider: 'other_provider' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.getByText('Novo')).toBeInTheDocument();
    expect(screen.queryByLabelText('Status externo')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Alterar status' })).not.toBeInTheDocument();
  });

  // D
  it('selecting the current external_status keeps Alterar status disabled', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.change(screen.getByLabelText('Status externo'), { target: { value: '1' } });
    expect(screen.getByRole('button', { name: 'Alterar status' })).toBeDisabled();
  });

  // E
  it('selecting a different status enables Alterar status', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.change(screen.getByLabelText('Status externo'), { target: { value: '5' } });
    expect(screen.getByRole('button', { name: 'Alterar status' })).not.toBeDisabled();
  });

  // F
  it('clicking Alterar status shows a confirmation naming current and target status, without calling the backend yet', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    expect(screen.getByText('Alterar status do chamado externo de "Novo" para "Resolvido"?')).toBeInTheDocument();
    expect(statusPostCalls().length).toBe(0);
  });

  // G
  it('canceling the confirmation makes no request', async () => {
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Cancelar' }));
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument());
    expect(statusPostCalls().length).toBe(0);
  });

  // H
  it('confirming sends exactly one POST', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await waitFor(() => expect(statusPostCalls().length).toBe(1));
  });

  // I
  it('request body contains only target_status', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await waitFor(() => expect(statusPostCalls().length).toBe(1));
    const [, body] = statusPostCalls()[0] as any[];
    expect(Object.keys(body)).toEqual(['target_status']);
    expect(body.target_status).toBe('5');
  });

  // J
  it('sends an Idempotency-Key header matching the required format', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await waitFor(() => expect(statusPostCalls().length).toBe(1));
    const [, , config] = statusPostCalls()[0] as any[];
    expect(config.headers['Idempotency-Key']).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
  });

  // K
  it('HTTP 200 updates the card from the response, without a second GET or a refresh call', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({
      status: 200,
      data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', sync_status: 'synced', last_synced_at: '2026-02-01T00:00:00Z', replayed: false, reconciled: false },
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    const getCallsBefore = vi.mocked(axios.get).mock.calls.filter((c) => (c[0] as string).endsWith('/ticket')).length;
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Resolvido');
    const getCallsAfter = vi.mocked(axios.get).mock.calls.filter((c) => (c[0] as string).endsWith('/ticket')).length;
    expect(getCallsAfter).toBe(getCallsBefore);
    expect(vi.mocked(axios.post).mock.calls.some((c) => (c[0] as string).endsWith('/ticket/refresh'))).toBe(false);
  });

  // L
  it('HTTP 200 with replayed=true shows recovery success, no second PUT, pending intent cleared', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, replayed: true, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Alteração já confirmada.');
    expect(statusPostCalls().length).toBe(1);
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull();
  });

  // M
  it('HTTP 200 with reconciled=true shows the reconciled success message and clears any pending intent', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, replayed: false, reconciled: true } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Status confirmado após verificação.');
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull();
  });

  // N
  it('rapid double click on Confirmar alteração produces exactly one request', async () => {
    mockRead({ status: 200, data: LINKED });
    let resolvePost: (v: unknown) => void = () => {};
    vi.mocked(axios.post).mockImplementation((url: string) => {
      if (!(url as string).endsWith('/ticket/status')) return Promise.reject({ response: { status: 404 } });
      return new Promise((resolve) => (resolvePost = resolve));
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    const confirmButton = screen.getByRole('button', { name: 'Confirmar alteração' });
    fireEvent.click(confirmButton);
    fireEvent.click(confirmButton);
    resolvePost({ data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    await screen.findByText('Status atualizado.');
    expect(statusPostCalls().length).toBe(1);
  });

  // FINAL IDEMPOTENCY RECOVERY FIX — section 9.A: the intent must be
  // durable BEFORE the POST resolves, not only after a caught failure.
  it('persists the pending intent BEFORE the POST resolves', async () => {
    mockRead({ status: 200, data: LINKED });
    vi.mocked(axios.post).mockImplementation((url: string) => {
      if (!(url as string).endsWith('/ticket/status')) return Promise.reject({ response: { status: 404 } });
      return new Promise(() => {}); // deliberately never resolves in this test
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await waitFor(() => {
      const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-status.${CONV}`) || 'null');
      expect(persisted).not.toBeNull();
      expect(persisted.targetStatus).toBe('5');
      expect(persisted.idempotencyKey).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    });
  });

  // section 9.F: a 409 on a FRESH confirm (no prior network failure) must
  // retain the exact same key that this confirm itself just persisted —
  // O2BH proves a 409 can mean THIS SAME key now owns a durable
  // outcome_unknown attempt.
  it('a 409 on a fresh confirm retains the exact same key that was just persisted', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 409 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Verificação necessária');
    const [, , config] = statusPostCalls()[0] as any[];
    const sentKey = config.headers['Idempotency-Key'];
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-status.${CONV}`) || '{}');
    expect(persisted.idempotencyKey).toBe(sentKey);
    expect(persisted.targetStatus).toBe('5');
  });

  // section 9.G/H: remounting after a 409 restores the recovery affordance
  // from storage, and Verificar alteração resends the exact same key.
  it('remounting after a 409 restores Verificar alteração, which resends the exact same key', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 409 });
    const { unmount } = renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Verificação necessária');
    const [, , config] = statusPostCalls()[0] as any[];
    const originalKey = config.headers['Idempotency-Key'];
    unmount();

    mockRead({ status: 200, data: LINKED }); // remount's own GET — same server identity
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(await screen.findByRole('button', { name: 'Verificar alteração' })).toBeInTheDocument();

    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: true, reconciled: false } });
    fireEvent.click(screen.getByRole('button', { name: 'Verificar alteração' }));
    await waitFor(() => expect(statusPostCalls().length).toBe(2));
    const [, body2, config2] = statusPostCalls()[1] as any[];
    expect(body2.target_status).toBe('5');
    expect(config2.headers['Idempotency-Key']).toBe(originalKey);
  });

  // section 9.I: while a matching unresolved intent exists, the ordinary
  // controls are replaced entirely — there is no way to mint a new key.
  it('while a matching unresolved intent exists, no new mutation key can be created', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent()));
    mockRead({ status: 200, data: LINKED }); // identity matches the stale intent (lt-1/k3g/28182)
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.queryByLabelText('Status externo')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Alterar status' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Verificar alteração' })).toBeInTheDocument();
  });

  // O, section 31 (network ambiguity persistence)
  it('a network failure persists the same target and Idempotency-Key for recovery', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation('network_error');
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Não foi possível confirmar a alteração.');
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-status.${CONV}`) || '{}');
    expect(persisted.targetStatus).toBe('5');
    expect(persisted.idempotencyKey).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
  });

  // P
  it('Verificar alteração resends the same target and the same Idempotency-Key', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation('network_error');
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Não foi possível confirmar a alteração.');
    const persisted = JSON.parse(localStorage.getItem(`omnira.ticket-status.${CONV}`) || '{}');

    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: true, reconciled: false } });
    fireEvent.click(screen.getByRole('button', { name: 'Verificar alteração' }));
    await waitFor(() => expect(statusPostCalls().length).toBe(2));
    const [, body, config] = statusPostCalls()[1] as any[];
    expect(body.target_status).toBe('5');
    expect(config.headers['Idempotency-Key']).toBe(persisted.idempotencyKey);
  });

  // Q
  it('409 on a persisted intent keeps the linked card, shows verification state, never falls back to CREATE, and triggers no automatic refresh', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation('network_error');
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Não foi possível confirmar a alteração.');

    mockStatusMutation({ status: 409 });
    fireEvent.click(screen.getByRole('button', { name: 'Verificar alteração' }));
    await screen.findByText('Verificação necessária');
    expect(screen.getByText('Chamado vinculado')).toBeInTheDocument();
    expect(screen.queryByLabelText('Empresa')).not.toBeInTheDocument();
    expect(vi.mocked(axios.post).mock.calls.some((c) => (c[0] as string).endsWith('/ticket/refresh'))).toBe(false);
  });

  // S
  it('422 does not automatically regenerate a key or retry, and clears the stale pending intent', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 422, data: 'idempotency key already used with different request' });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText(/Não foi possível confirmar esta alteração de status/);
    expect(statusPostCalls().length).toBe(1);
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull();
    expect(screen.getByRole('button', { name: 'Alterar status' })).toBeInTheDocument();
  });

  // T — section 5: a definitive authorization failure clears the intent.
  it('403 disables mutation interaction without any optimistic status change, and clears the pending intent', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 403 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Sem permissão para alterar o status deste chamado.');
    expect(screen.getByText('Novo')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Alterar status' })).toBeDisabled();
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull();
  });

  // U — section 5: every /ticket/status 503 path is confirmed (from the
  // backend's own source ordering — runtime resolution happens before
  // Acquire) to occur strictly before any provider write, so it is safe to
  // clear the intent and allow a fresh later action.
  it('503 keeps the linked card visible with no optimistic change, and clears the pending intent', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 503 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Não foi possível confirmar a alteração agora.');
    expect(screen.getByText('Chamado vinculado')).toBeInTheDocument();
    expect(screen.getByText('28182')).toBeInTheDocument();
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull();
    expect(screen.getByRole('button', { name: 'Alterar status' })).not.toBeDisabled();
  });

  // V
  it('a conversation switch before the status response arrives ignores the late response', async () => {
    mockRead({ status: 200, data: LINKED });
    vi.mocked(axios.post).mockImplementation((url: string) => {
      if (!(url as string).endsWith('/ticket/status')) return Promise.reject({ response: { status: 404 } });
      return new Promise(() => {}); // never resolves within this test
    });

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
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));

    fireEvent.click(screen.getByRole('button', { name: 'switch conversation' }));
    await screen.findByText('Chamado vinculado'); // conversation c-2's own GET resolves independently
    expect(screen.queryByRole('dialog')).not.toBeInTheDocument();
    expect(screen.getByText('Novo')).toBeInTheDocument(); // c-2's own (unaffected) projection
  });

  // W
  it('a successful status mutation never introduces local workflow status controls', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Status atualizado.');
    expect(screen.queryByRole('button', { name: /Trabalhando/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /^Resolvido$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: /Fechar/i })).not.toBeInTheDocument();
  });

  // X
  it('existing Atualizar (refresh) behavior still works alongside the new status controls', async () => {
    mockRead({ status: 200, data: LINKED });
    mockRefresh({ status: 200, data: { ...LINKED, external_status: '2', external_status_label: 'Em atendimento' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    fireEvent.click(screen.getByRole('button', { name: 'Atualizar' }));
    await screen.findByText('Em atendimento');
  });

  // Y
  it('a status mutation never automatically calls /ticket/refresh', async () => {
    mockRead({ status: 200, data: LINKED });
    mockStatusMutation({ status: 200, data: { ...LINKED, external_status: '5', external_status_label: 'Resolvido', replayed: false, reconciled: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await openConfirmFor('5');
    fireEvent.click(screen.getByRole('button', { name: 'Confirmar alteração' }));
    await screen.findByText('Status atualizado.');
    expect(vi.mocked(axios.post).mock.calls.some((c) => (c[0] as string).endsWith('/ticket/refresh'))).toBe(false);
  });

  // Z, AA, AB: covered by the pre-existing describe blocks above (create
  // form / O1F projection authority / O1RF refresh), which all still pass
  // unmodified in this same file — no separate re-assertion needed here.

  // Section 31/section-1 (stale intent cleanup): persisted-intent identity
  // guard (section 10) — a mismatch must not merely be ignored, it must be
  // explicitly DELETED from both component state and localStorage once the
  // canonical projection has actually resolved.

  // A
  it('a persisted intent matching the current server identity IS retained and offered for verification', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent()));
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    expect(screen.getByRole('button', { name: 'Verificar alteração' })).toBeInTheDocument();
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).not.toBeNull();
  });

  // B
  it('a persisted intent for a different local_ticket_id is deleted from state and localStorage', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent({ localTicketId: 'different-ticket' })));
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Alterar status' })).toBeInTheDocument();
  });

  // C
  it('a persisted intent for a different provider is deleted from state and localStorage', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent({ provider: 'other_provider' })));
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
  });

  // D
  it('a persisted intent for a different external_ticket_id is deleted from state and localStorage', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent({ externalTicketId: 'different-ext-id' })));
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
  });

  it('a persisted intent for a different conversation_id is deleted from state and localStorage', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent({ conversationId: 'different-conv' })));
    mockRead({ status: 200, data: LINKED });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Chamado vinculado');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
  });

  // E
  it('a persisted intent is NOT prematurely deleted while the initial projection is still loading', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent({ localTicketId: 'different-ticket' })));
    let resolveGet: (v: unknown) => void = () => {};
    vi.mocked(axios.get).mockImplementation((url: string) => {
      if (url.endsWith('/crm/companies')) return Promise.resolve({ data: { items: [COMPANY] } });
      if (url.endsWith('/ticket')) return new Promise((resolve) => (resolveGet = resolve));
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<TicketPanel conversationId={CONV} />);
    expect(screen.getByText('Carregando chamado…')).toBeInTheDocument();
    // Still loading — server authority not yet known — nothing may be
    // deleted on its account, even though this intent will turn out to
    // mismatch once the read resolves.
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).not.toBeNull();

    resolveGet({ data: LINKED }); // resolves to a MISMATCHING identity (local_ticket_id: 'lt-1' vs stored 'different-ticket')
    await screen.findByText('Chamado vinculado');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
  });

  // F
  it('linked=false (canonical no-link): a stale mutation intent is deleted and cannot survive as recovery authority', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent()));
    mockRead({ status: 200, data: { local_ticket_id: 'lt-1', linked: false } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByLabelText('Empresa');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
    expect(screen.queryByText('Chamado vinculado')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
  });

  it('404 (canonical no active ticket): a stale mutation intent is deleted', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent()));
    mockRead({ status: 404 });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Não há chamado ativo nesta conversa para vincular ao ERP.');
    await waitFor(() => expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).toBeNull());
  });

  it('409 inconsistent linkage: a stale mutation intent is conservatively RETAINED (no positive proof of a different identity), never used to establish linkage', async () => {
    localStorage.setItem(`omnira.ticket-status.${CONV}`, JSON.stringify(stalePersistedStatusIntent()));
    mockRead({ status: 409, data: { error: 'inconsistent', code: 'TICKET_INCONSISTENT_LINK' } });
    renderAt(<TicketPanel conversationId={CONV} />);
    await screen.findByText('Verificação necessária');
    expect(localStorage.getItem(`omnira.ticket-status.${CONV}`)).not.toBeNull();
    expect(screen.queryByText('Chamado vinculado')).not.toBeInTheDocument();
    expect(screen.queryByRole('button', { name: 'Verificar alteração' })).not.toBeInTheDocument();
  });

  // Section 32: API client contract (no dedicated tickets.ts unit test file
  // exists in this codebase — createExternalTicket/readConversationTicket/
  // refreshConversationTicket are likewise only exercised through this
  // component's own tests, per existing convention). Covered by tests H-J
  // above: POST, path ending in /ticket/status, Idempotency-Key header,
  // body = {"target_status": "5"} only (no provider/external_ticket_id/
  // statusId/actor_user_id).
});
