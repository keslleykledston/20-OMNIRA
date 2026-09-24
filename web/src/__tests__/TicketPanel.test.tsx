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

function mockCompanies(items: unknown[] = [COMPANY]) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/integrations/companies')) return { data: { items } };
    return Promise.reject({ response: { status: 404 } });
  });
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
  it('already_linked state persists across remount using localStorage', async () => {
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

    renderAt(<TicketPanel conversationId={CONV} />);
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
