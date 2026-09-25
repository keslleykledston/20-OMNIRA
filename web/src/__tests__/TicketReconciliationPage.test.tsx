import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, fireEvent, waitFor } from '@testing-library/react';
import axios from 'axios';
import TicketReconciliationPage from '../pages/TicketReconciliationPage';
import { renderAt, setSession, TENANT } from './testUtils';

// PRODUCT.7A2: read-only ticket reconciliation UI. All HTTP is mocked — no
// live backend/K3G call is ever made from these tests. This module never
// imports axios.post/put/patch/delete, matching lib/reconciliation.ts.
vi.mock('axios');

function actor(over: object = {}) {
  return { id: 'u-1', display_name: 'Alice Agent', email: 'alice@example.com', ...over };
}

function createAttempt(over: object = {}) {
  return {
    id: 'ca-' + Math.random().toString(36).slice(2),
    state: 'in_flight',
    conversation_id: 'conv-1',
    local_ticket_id: null,
    local_ticket_subject: null,
    provider: null,
    external_ticket_id: null,
    actor: actor(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    projection_synced_at: null,
    idempotency_key_redacted: 'abcd1234…',
    ...over,
  };
}

function statusAttempt(over: object = {}) {
  return {
    id: 'sa-' + Math.random().toString(36).slice(2),
    state: 'in_flight',
    conversation_id: 'conv-2',
    local_ticket_id: 'lt-1',
    local_ticket_subject: 'Preciso de ajuda',
    provider: 'k3g',
    external_ticket_id: '9115',
    target_status: '5',
    confirmed_external_status: null,
    confirmed_external_status_label: null,
    actor: actor(),
    created_at: new Date().toISOString(),
    updated_at: new Date().toISOString(),
    projection_synced_at: null,
    idempotency_key_redacted: 'wxyz5678…',
    ...over,
  };
}

function page(items: unknown[] = [], over: object = {}) {
  return { items, has_more: false, count: items.length, limit: 20, ...over };
}

function mockReconciliation({
  access = ['ticket.reconcile'],
  create = page(),
  status = page(),
}: { access?: string[]; create?: any; status?: any } = {}) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { permissions: access } };
    if (url.endsWith('/ticket-reconciliation/create')) return { data: create };
    if (url.endsWith('/ticket-reconciliation/status')) return { data: status };
    return Promise.reject({ response: { status: 404 } });
  });
}

function lastCallTo(pathSuffix: string) {
  const calls = vi.mocked(axios.get).mock.calls.filter(([url]) => (url as string).endsWith(pathSuffix));
  return calls[calls.length - 1];
}

beforeEach(() => {
  vi.clearAllMocks();
  setSession();
});

describe('TicketReconciliationPage — access', () => {
  // A
  it('a user with ticket.reconcile sees the page (tabs, no permission wall)', async () => {
    mockReconciliation();
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist', { name: 'Tipo de operação' });
    expect(screen.queryByText('Você não tem permissão para visualizar a reconciliação de chamados.')).not.toBeInTheDocument();
  });

  // B
  it('a user without ticket.reconcile sees no operational surface', async () => {
    mockReconciliation({ access: [] });
    renderAt(<TicketReconciliationPage />);
    await screen.findByText('Você não tem permissão para visualizar a reconciliação de chamados.');
    expect(screen.queryByRole('tablist')).not.toBeInTheDocument();
    // No reconciliation GET is even attempted once access is known denied.
    await waitFor(() => {
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith('/ticket-reconciliation/create'))).toBe(false);
      expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith('/ticket-reconciliation/status'))).toBe(false);
    });
  });
});

describe('TicketReconciliationPage — tabs call the right endpoint only', () => {
  // C
  it('Criação tab calls only the create endpoint', async () => {
    mockReconciliation({ create: page([createAttempt({ id: 'ca-only' })]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tab', { name: 'Criação', selected: true });
    await waitFor(() => expect(lastCallTo('/ticket-reconciliation/create')).toBeDefined());
    expect(vi.mocked(axios.get).mock.calls.some(([url]) => (url as string).endsWith('/ticket-reconciliation/status'))).toBe(false);
  });

  // D
  it('Status tab calls only the status endpoint', async () => {
    mockReconciliation({ status: page([statusAttempt({ id: 'sa-only' })]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    await waitFor(() => expect(lastCallTo('/ticket-reconciliation/status')).toBeDefined());
  });
});

describe('TicketReconciliationPage — operational state UX', () => {
  // E
  it('outcome_unknown shows the distinct "Verificação necessária" state', async () => {
    mockReconciliation({ create: page([createAttempt({ state: 'outcome_unknown' })]) });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('Verificação necessária')).toBeInTheDocument();
  });

  // F
  it('in_flight shows elapsed age', async () => {
    const created = new Date(Date.now() - 18 * 60_000).toISOString();
    mockReconciliation({ create: page([createAttempt({ state: 'in_flight', created_at: created })]) });
    renderAt(<TicketReconciliationPage />);
    expect((await screen.findAllByText(/Em andamento há 1[7-9] min/)).length).toBeGreaterThan(0);
  });

  // G
  it('confirmed_failure shows "Falha confirmada"', async () => {
    mockReconciliation({ create: page([createAttempt({ state: 'confirmed_failure' })]) });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('Falha confirmada')).toBeInTheDocument();
  });

  // H
  it('confirmed_success + synced projection shows "Confirmado / sincronizado"', async () => {
    mockReconciliation({
      create: page([createAttempt({ state: 'confirmed_success', provider: 'k3g', external_ticket_id: '1', projection_synced_at: new Date().toISOString() })]),
    });
    renderAt(<TicketReconciliationPage />);
    expect((await screen.findAllByText('Confirmado / sincronizado')).length).toBeGreaterThan(0);
  });

  // I
  it('confirmed_success + unsynced projection shows a distinct pending state', async () => {
    mockReconciliation({
      create: page([createAttempt({ state: 'confirmed_success', provider: 'k3g', external_ticket_id: '1', projection_synced_at: null })]),
    });
    renderAt(<TicketReconciliationPage />);
    expect((await screen.findAllByText('Confirmado / projeção pendente')).length).toBeGreaterThan(0);
    expect(screen.queryByText('Confirmado / sincronizado')).not.toBeInTheDocument();
  });
});

describe('TicketReconciliationPage — safe rendering of nullable/typed fields', () => {
  // J
  it('a create attempt with null local_ticket_id/provider/external_id renders safely (no crash, "—" placeholders)', async () => {
    mockReconciliation({ create: page([createAttempt({ local_ticket_id: null, provider: null, external_ticket_id: null })]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findAllByText('Alice Agent'); // wait for the row itself to render
    expect(screen.getAllByText('—').length).toBeGreaterThan(0);
  });

  // K
  it('status target/result fields render (target_status translated, confirmed label shown)', async () => {
    mockReconciliation({
      status: page([statusAttempt({
        state: 'confirmed_success', target_status: '5',
        confirmed_external_status: '5', confirmed_external_status_label: 'Resolvido',
        projection_synced_at: new Date().toISOString(),
      })]),
    });
    renderAt(<TicketReconciliationPage />);
    fireEvent.click(await screen.findByRole('tab', { name: 'Status' }));
    expect(await screen.findAllByText('Resolvido')).not.toHaveLength(0); // target label (k3g "5" -> Resolvido) AND confirmed label
  });

  // L
  it('actor display projection renders (display_name preferred, email fallback)', async () => {
    mockReconciliation({
      create: page([
        createAttempt({ actor: actor({ display_name: 'Bruno Support', email: 'bruno@example.com' }) }),
        createAttempt({ actor: actor({ display_name: '', email: 'no-name@example.com' }) }),
      ]),
    });
    renderAt(<TicketReconciliationPage />);
    expect((await screen.findAllByText('Bruno Support')).length).toBeGreaterThan(0);
    expect((await screen.findAllByText('no-name@example.com')).length).toBeGreaterThan(0);
  });
});

describe('TicketReconciliationPage — idempotency key / request_hash', () => {
  // M
  it('shows only the already-redacted key from the API, never a reconstructed/full value', async () => {
    mockReconciliation({ create: page([createAttempt({ idempotency_key_redacted: 'abcd1234…' })]) });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('abcd1234…')).toBeInTheDocument();
    // No 36+ char UUID-shaped full key ever rendered anywhere.
    expect(document.body.textContent).not.toMatch(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/);
  });

  // N
  it('never renders/expects a request_hash field', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    expect(document.body.textContent).not.toContain('request_hash');
  });
});

describe('TicketReconciliationPage — filters wired to the backend', () => {
  // O
  it('state filter is sent to the create endpoint', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.change(screen.getByLabelText('Filtrar por estado'), { target: { value: 'outcome_unknown' } });
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ state: 'outcome_unknown' }) });
    });
  });

  // P
  it('provider filter is sent to the create endpoint', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.change(screen.getByLabelText('Filtrar por provedor'), { target: { value: 'k3g' } });
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ provider: 'k3g' }) });
    });
  });

  // Q
  it('external_ticket_id filter is sent to the create endpoint', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.change(screen.getByLabelText('Filtrar por ID externo'), { target: { value: '28182' } });
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ external_ticket_id: '28182' }) });
    });
  });

  // R
  it('date range filters are sent to the create endpoint', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.change(screen.getByLabelText('Data inicial'), { target: { value: '2026-01-01' } });
    fireEvent.change(screen.getByLabelText('Data final'), { target: { value: '2026-01-31' } });
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect(call?.[1]).toMatchObject({
        params: expect.objectContaining({ created_from: '2026-01-01T00:00:00Z', created_to: '2026-01-31T23:59:59Z' }),
      });
    });
  });
});

describe('TicketReconciliationPage — pagination', () => {
  // S
  it('cursor pagination advances using has_more/next_cursor', async () => {
    mockReconciliation({ create: page([createAttempt({ id: 'ca-1' })], { has_more: true, next_cursor: 'CURSOR1' }) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    const nextButton = await screen.findByRole('button', { name: /Próxima/ });
    expect(nextButton).not.toBeDisabled();

    mockReconciliation({ create: page([createAttempt({ id: 'ca-2' })], { has_more: false }) });
    fireEvent.click(nextButton);
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ cursor: 'CURSOR1' }) });
    });
  });

  // T
  it('switching tabs never mixes create/status cursors', async () => {
    mockReconciliation({
      create: page([createAttempt({ id: 'ca-1' })], { has_more: true, next_cursor: 'CREATE-CURSOR' }),
      status: page([statusAttempt({ id: 'sa-1' })], { has_more: true, next_cursor: 'STATUS-CURSOR' }),
    });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.click(await screen.findByRole('button', { name: /Próxima/ })); // advance create tab
    await waitFor(() => {
      expect(lastCallTo('/ticket-reconciliation/create')?.[1]).toMatchObject({ params: expect.objectContaining({ cursor: 'CREATE-CURSOR' }) });
    });

    fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    await waitFor(() => expect(lastCallTo('/ticket-reconciliation/status')).toBeDefined());
    // The FIRST status call must never carry the create tab's cursor.
    const firstStatusCall = vi.mocked(axios.get).mock.calls.find(([url]) => (url as string).endsWith('/ticket-reconciliation/status'));
    expect((firstStatusCall?.[1] as any)?.params?.cursor).toBeUndefined();
  });
});

describe('TicketReconciliationPage — conversation navigation', () => {
  // U
  it('Abrir conversa links to the exact frozen deep-link contract', async () => {
    mockReconciliation({ create: page([createAttempt({ conversation_id: 'conv-abc' })]) });
    renderAt(<TicketReconciliationPage />);
    const links = await screen.findAllByRole('link', { name: 'Abrir conversa' });
    expect(links[0]).toHaveAttribute('href', '/inbox?conversation_id=conv-abc');
  });
});

describe('TicketReconciliationPage — no mutation surface', () => {
  // V
  it('renders no mutation controls of any kind', async () => {
    mockReconciliation({
      create: page([createAttempt({ state: 'outcome_unknown' })]),
      status: page([statusAttempt({ state: 'outcome_unknown' })]),
    });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    for (const name of ['Retry', 'Reconciliar', 'Desbloquear', 'Forçar sucesso', 'Forçar falha', 'Limpar tentativa', 'Atualizar provider']) {
      expect(screen.queryByRole('button', { name: new RegExp(name, 'i') })).not.toBeInTheDocument();
    }
  });

  // W
  it('never calls a mutation HTTP method against ticket-reconciliation (or anything)', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    expect(vi.mocked(axios.post)).not.toHaveBeenCalled();
    expect(vi.mocked(axios.put)).not.toHaveBeenCalled();
    expect(vi.mocked(axios.patch)).not.toHaveBeenCalled();
    expect(vi.mocked(axios.delete)).not.toHaveBeenCalled();
  });

  // X
  it('no provider/TicketingConnector call is reachable from this page (only the two GET endpoints are ever hit)', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    fireEvent.click(screen.getByRole('tab', { name: 'Status' }));
    await waitFor(() => expect(lastCallTo('/ticket-reconciliation/status')).toBeDefined());
    const urls = vi.mocked(axios.get).mock.calls.map(([url]) => url as string);
    for (const url of urls) {
      expect(url.endsWith('/me/access') || url.includes('/ticket-reconciliation/')).toBe(true);
    }
  });
});

describe('TicketReconciliationPage — empty/error states', () => {
  // Y
  it('shows a calm, non-alarmist empty state when there are no attempts', async () => {
    mockReconciliation({ create: page([]) });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('Nenhuma tentativa de criação')).toBeInTheDocument();
  });

  // Z
  it('shows the existing ErrorState convention on a backend failure', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { permissions: ['ticket.reconcile'] } };
      if (url.endsWith('/ticket-reconciliation/create')) return Promise.reject({ response: { status: 500 } });
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('Não foi possível carregar os dados de reconciliação')).toBeInTheDocument();
  });

  it('shows a permission-specific message on a 403 from the backend itself', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { permissions: ['ticket.reconcile'] } };
      if (url.endsWith('/ticket-reconciliation/create')) return Promise.reject({ response: { status: 403 } });
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<TicketReconciliationPage />);
    expect(await screen.findByText('Você não tem permissão para visualizar a reconciliação de chamados deste tenant.')).toBeInTheDocument();
  });
});

describe('TicketReconciliationPage — accessibility', () => {
  it('tabs are keyboard accessible (ArrowRight moves selection)', async () => {
    mockReconciliation({ create: page([createAttempt()]), status: page([statusAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    const createTab = await screen.findByRole('tab', { name: 'Criação' });
    createTab.focus();
    fireEvent.keyDown(createTab, { key: 'ArrowRight' });
    await waitFor(() => expect(lastCallTo('/ticket-reconciliation/status')).toBeDefined());
  });

  it('filters have accessible labels', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await screen.findByRole('tablist');
    expect(screen.getByLabelText('Filtrar por estado')).toBeInTheDocument();
    expect(screen.getByLabelText('Filtrar por provedor')).toBeInTheDocument();
    expect(screen.getByLabelText('Filtrar por ID externo')).toBeInTheDocument();
    expect(screen.getByLabelText('Data inicial')).toBeInTheDocument();
    expect(screen.getByLabelText('Data final')).toBeInTheDocument();
  });
});

describe('TicketReconciliationPage — tenant scope (implicit via session/URL)', () => {
  it('requests are scoped to the current tenant path', async () => {
    mockReconciliation({ create: page([createAttempt()]) });
    renderAt(<TicketReconciliationPage />);
    await waitFor(() => {
      const call = lastCallTo('/ticket-reconciliation/create');
      expect((call?.[0] as string)).toContain(`/tenants/${TENANT}/ticket-reconciliation/create`);
    });
  });
});
