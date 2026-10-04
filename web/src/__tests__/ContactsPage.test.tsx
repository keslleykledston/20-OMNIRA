import { beforeEach, describe, expect, it, vi } from 'vitest';
import { render, screen, waitFor, within } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { MemoryRouter, Route, Routes, useLocation } from 'react-router-dom';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ContactsPage, { formatPhone, initials } from '../pages/ContactsPage';
import ContactDetailPage from '../pages/ContactDetailPage';
import { mockGets, renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');

const CONTACTS = `/api/v1/tenants/${TENANT}/contacts`;

const contact = (over: object = {}) => ({
  id: 'c1111111-1111-1111-1111-111111111111',
  display_name: 'Ana Souza',
  phone_e164: '+5511998887766',
  email: 'ana@example.com',
  status: 'active',
  kind: 'other',
  created_at: '2026-01-10T12:00:00Z',
  updated_at: '2026-02-20T15:30:00Z',
  last_interaction_at: '2026-02-20T15:30:00Z',
  channels: ['whatsapp'],
  open_conversation_count: 2,
  ...over,
});

const page = (items: object[], over: object = {}) => ({
  items,
  has_more: false,
  count: items.length,
  limit: 20,
  ...over,
});

describe('formatPhone', () => {
  it('formats a Brazilian mobile', () => {
    expect(formatPhone('+5511998887766')).toBe('+55 11 99888-7766');
  });

  // The API stores E.164 from any country; anything we cannot parse is shown raw
  // rather than mangled.
  it('leaves a non-Brazilian number untouched', () => {
    expect(formatPhone('+441632960961')).toBe('+441632960961');
  });
});

describe('initials', () => {
  it('uses first and last name', () => {
    expect(initials('Ana Souza')).toBe('AS');
  });
  it('falls back to two letters for a single name', () => {
    expect(initials('Ana')).toBe('AN');
  });
  it('survives an empty name', () => {
    expect(initials('   ')).toBe('?');
  });
});

describe('ContactsPage', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  // The table and the mobile cards are both in the DOM, hidden from each other
  // by CSS that jsdom does not apply — hence findAllByText throughout.
  it('lists the tenant contacts', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([contact()]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('+55 11 99888-7766').length).toBeGreaterThan(0);
  });

  it('shows the channel, the open conversations and a dash when there is no interaction', async () => {
    vi.mocked(axios.get).mockResolvedValue({
      data: page([
        contact({ id: 'a', display_name: 'Com Canal' }),
        contact({ id: 'b', display_name: 'Sem Nada', channels: [], last_interaction_at: null, open_conversation_count: 0 }),
      ]),
    });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    await screen.findAllByText('Com Canal');
    expect(screen.getAllByText('WhatsApp').length).toBeGreaterThan(0);
    for (const header of ['Canais', 'Última interação', 'Conversas abertas']) {
      expect(screen.getByText(header)).toBeInTheDocument();
    }
    // The contact without any conversation shows em dashes, never an invented value.
    expect(screen.getAllByText('—').length).toBeGreaterThanOrEqual(2);
  });

  it('shows an empty state when the tenant has no contacts', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect(await screen.findByText('Nenhum contato ainda')).toBeInTheDocument();
  });

  it('surfaces a failure with a retry', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 500 } });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    expect(await screen.findByText('Não foi possível carregar os contatos')).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument();
  });

  it('walks pages with the cursor and can come back', async () => {
    const first = page([contact({ id: 'c1', display_name: 'Ana Souza' })], {
      has_more: true,
      next_cursor: 'CURSOR_2',
    });
    const second = page([contact({ id: 'c2', display_name: 'Bruno Lima' })]);

    vi.mocked(axios.get).mockImplementation(async (_url: string, config?: any) => ({
      data: config?.params?.cursor === 'CURSOR_2' ? second : first,
    }));

    renderAt(<ContactsPage />, '/contacts', '/contacts');
    await screen.findAllByText('Ana Souza');

    const next = screen.getByRole('button', { name: 'Próxima' });
    expect(next).toBeEnabled();
    await userEvent.click(next);

    expect((await screen.findAllByText('Bruno Lima')).length).toBeGreaterThan(0);
    // Last page: there is nowhere further to go.
    expect(screen.getByRole('button', { name: 'Próxima' })).toBeDisabled();

    await userEvent.click(screen.getByRole('button', { name: 'Anterior' }));
    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
  });

  it('disables Anterior on the first page', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: page([contact()]) });
    renderAt(<ContactsPage />, '/contacts', '/contacts');

    await screen.findAllByText('Ana Souza');
    expect(screen.getByRole('button', { name: 'Anterior' })).toBeDisabled();
  });
});

describe('ContactDetailPage (Contact 360)', () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    setSession();
  });

  const conversation = (over: object = {}) => ({
    id: 'cv1',
    status: 'open',
    title: '',
    channel: 'whatsapp',
    provider: 'waha',
    assigned_to_user_id: null,
    message_count: 3,
    last_message: {
      direction: 'inbound',
      message_type: 'text',
      body_preview: 'Quero a segunda via',
      created_at: '2026-02-20T15:30:00Z',
    },
    created_at: '2026-02-20T15:00:00Z',
    updated_at: '2026-02-20T15:30:00Z',
    ...over,
  });

  const ticket = (over: object = {}) => ({
    id: 't1',
    conversation_id: 'cv1',
    subject: 'Problema com pagamento',
    status: 'in_progress',
    priority: 'high',
    assigned_to: null,
    provider: null,
    external_ticket_id: null,
    external_status_label: null,
    created_at: '2026-02-20T15:00:00Z',
    updated_at: '2026-02-20T15:30:00Z',
    ...over,
  });

  const subpage = (items: object[], over: object = {}) => page(items, over);

  // Answers by URL suffix. A value that is an Error-like { reject } is rejected.
  const serve = (routes: { contact?: object; conversations?: object; tickets?: object }) => {
    const answer = (value: any) =>
      value && value.reject ? Promise.reject(value.reject) : Promise.resolve({ data: value });
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/conversations')) return answer(routes.conversations ?? subpage([]));
      if (url.endsWith('/tickets')) return answer(routes.tickets ?? subpage([]));
      return answer(routes.contact ?? contact());
    });
  };

  const detail = (id = 'c1') =>
    renderAt(<ContactDetailPage />, `/contacts/${id}`, '/contacts/:contactId');

  it('shows the contact profile', async () => {
    serve({});
    detail();

    expect(await screen.findByRole('heading', { name: 'Ana Souza' })).toBeInTheDocument();
    expect(screen.getByText('ana@example.com')).toBeInTheDocument();
    expect(screen.getByText('Ativo')).toBeInTheDocument();
    expect(screen.getByText(/Cliente desde/)).toBeInTheDocument();
  });

  it('marks a missing email instead of inventing one', async () => {
    serve({ contact: contact({ email: '' }) });
    detail();

    expect(await screen.findByText('Não informado')).toBeInTheDocument();
  });

  // A contact of another tenant answers 404 exactly like an unknown id, so the
  // UI must not imply the record exists elsewhere.
  it('treats 404 as simply not found', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 404 } });
    detail();

    expect(await screen.findByText('Contato não encontrado.')).toBeInTheDocument();
  });

  it('copies the raw E.164 phone, not the formatted one', async () => {
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.assign(navigator, { clipboard: { writeText } });
    serve({});
    detail();

    await screen.findByRole('heading', { name: 'Ana Souza' });
    await userEvent.click(screen.getByRole('button', { name: 'Copiar telefone' }));

    await waitFor(() => expect(writeText).toHaveBeenCalledWith('+5511998887766'));
  });

  it('shows the conversation history with who spoke last, the channel and the count', async () => {
    serve({
      conversations: subpage([
        conversation(),
        conversation({
          id: 'cv2',
          status: 'closed',
          message_count: 1,
          last_message: { direction: 'outbound', message_type: 'image', body_preview: '', created_at: '2026-02-19T10:00:00Z' },
        }),
        conversation({ id: 'cv3', channel: null, message_count: 0, last_message: null }),
      ]),
    });
    detail();

    expect(await screen.findByText('Ana: Quero a segunda via')).toBeInTheDocument();
    // A media message without a caption names its kind instead of leaving a blank line.
    expect(screen.getByText('Você: Imagem')).toBeInTheDocument();
    expect(screen.getByText('Sem mensagens')).toBeInTheDocument();
    expect(screen.getByText('3 mensagens')).toBeInTheDocument();
    expect(screen.getByText('1 mensagem')).toBeInTheDocument();
    expect(screen.getByText('Sem canal')).toBeInTheDocument();
    // cv1 and cv3 are open, cv2 is closed.
    expect(screen.getAllByText('Aberta')).toHaveLength(2);
    expect(screen.getAllByText('Encerrada')).toHaveLength(1);
  });

  it('opens the conversation in the Inbox through the frozen deep link', async () => {
    serve({ conversations: subpage([conversation({ id: 'cv-9' })]) });
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    function Where() {
      const l = useLocation();
      return <div data-testid="where">{l.pathname + l.search}</div>;
    }
    render(
      <QueryClientProvider client={queryClient}>
        <MemoryRouter initialEntries={['/contacts/c1']}>
          <Routes>
            <Route path="/contacts/:contactId" element={<ContactDetailPage />} />
            <Route path="/inbox" element={<Where />} />
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    );

    await userEvent.click(await screen.findByText('Ana: Quero a segunda via'));
    expect(await screen.findByTestId('where')).toHaveTextContent('/inbox?conversation_id=cv-9');
  });

  it('lists the tickets and counts only the active ones', async () => {
    serve({
      tickets: subpage([
        ticket(),
        ticket({ id: 't2', subject: 'Troca de produto', status: 'resolved', priority: 'low' }),
        ticket({ id: 't3', subject: 'Dúvida sobre garantia', status: 'waiting', priority: 'medium', external_ticket_id: '1042' }),
      ]),
    });
    detail();

    expect((await screen.findAllByText(/Problema com pagamento/)).length).toBeGreaterThan(0);
    expect(screen.getAllByText('#1042').length).toBeGreaterThan(0);
    const card = screen.getByRole('group', { name: 'Tickets ativos' });
    // in_progress + waiting are active; resolved is not.
    expect(within(card).getByText('2')).toBeInTheDocument();
  });

  // Real tickets can be created without a subject; a blank cell reads as a bug.
  it('labels a ticket that has no subject instead of leaving the cell blank', async () => {
    serve({ tickets: subpage([ticket({ subject: '' })]) });
    detail();

    expect((await screen.findAllByText('Sem assunto')).length).toBeGreaterThan(0);
  });

  // ticket.read comes from the role matrix: a role without it is a normal state,
  // not an error, and the screen must not pretend there are zero tickets.
  it('explains a missing ticket.read permission instead of showing an empty list', async () => {
    serve({ tickets: { reject: { response: { status: 403 } } } });
    detail();

    expect(await screen.findByText('Você não tem permissão para ver tickets.')).toBeInTheDocument();
    expect(screen.queryByText('Nenhum ticket para este contato.')).not.toBeInTheDocument();
    const card = screen.getByRole('group', { name: 'Tickets ativos' });
    expect(within(card).getByText('—')).toBeInTheDocument();
  });

  it('keeps the rest of the screen usable when tickets fail, with a retry', async () => {
    serve({ tickets: { reject: { response: { status: 500 } } } });
    detail();

    expect(await screen.findByText('Não foi possível carregar os tickets.')).toBeInTheDocument();
    expect(screen.getAllByRole('button', { name: 'Tentar novamente' }).length).toBeGreaterThan(0);
    expect(screen.getByRole('heading', { name: 'Ana Souza' })).toBeInTheDocument();
  });

  it('says so when the contact has no conversations or tickets yet', async () => {
    serve({ contact: contact({ channels: [], last_interaction_at: null, open_conversation_count: 0 }) });
    detail();

    expect(await screen.findByText('Nenhuma conversa ainda.')).toBeInTheDocument();
    expect(screen.getByText('Nenhum ticket para este contato.')).toBeInTheDocument();
  });

  it('shows no data the backend does not have (tags, owner, document, notes)', async () => {
    serve({});
    detail();

    await screen.findByRole('heading', { name: 'Ana Souza' });
    for (const invented of ['Tags', 'Observações', 'CPF', 'Responsável', 'Iniciar conversa', 'Criar ticket']) {
      expect(screen.queryByText(invented)).not.toBeInTheDocument();
    }
  });
});

describe('ContactsPage — search, filters and kind (ADR-0014)', () => {
  const listCalls = () =>
    vi.mocked(axios.get).mock.calls
      .filter(([url]) => (url as string).endsWith('/contacts'))
      .map(([, config]) => ((config as { params?: Record<string, unknown> } | undefined)?.params ?? {}));

  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
  });

  it('shows each contact\'s kind next to its status', async () => {
    mockGets({
      [CONTACTS]: page([
        contact({ id: 'a', display_name: 'Cliente Ouro', kind: 'customer' }),
        contact({ id: 'b', display_name: 'Promo Chata', kind: 'spam' }),
        contact({ id: 'c', display_name: 'Colega', kind: 'other' }),
      ]),
    });
    renderAt(<ContactsPage />, '/contacts');
    await screen.findAllByText('Cliente Ouro');
    const rows = screen.getAllByRole('row');
    expect(within(rows.find((r) => r.textContent?.includes('Cliente Ouro'))!).getByText('Cliente')).toBeInTheDocument();
    expect(within(rows.find((r) => r.textContent?.includes('Promo Chata'))!).getByText('Spam')).toBeInTheDocument();
    expect(within(rows.find((r) => r.textContent?.includes('Colega'))!).getByText('Outros')).toBeInTheDocument();
  });

  it('searches on the server after the typing pauses (not once per key) and combines the filters', async () => {
    const user = userEvent.setup();
    mockGets({ [CONTACTS]: page([contact()]) });
    renderAt(<ContactsPage />, '/contacts');
    await screen.findAllByText('Ana Souza');
    expect(listCalls()[0]).toEqual({ limit: 20 }); // no filter sent by default

    await user.type(screen.getByLabelText('Buscar contato'), 'maria');
    await waitFor(() => expect(listCalls().some((p) => p.q === 'maria')).toBe(true));
    expect(listCalls().filter((p) => typeof p.q === 'string' && p.q !== 'maria').length).toBe(0);

    await user.selectOptions(screen.getByLabelText('Tipo'), 'spam');
    await user.selectOptions(screen.getByLabelText('Status'), 'blocked');
    await waitFor(() => expect(listCalls().some((p) => p.q === 'maria' && p.kind === 'spam' && p.status === 'blocked')).toBe(true));
  });

  it('goes back to the first page when a filter changes (a cursor only fits the filters that made it)', async () => {
    const user = userEvent.setup();
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      if (!(url as string).endsWith('/contacts')) return Promise.reject({ response: { status: 404 } });
      return { data: config?.params?.cursor ? page([contact({ id: 'p2', display_name: 'Segunda Pagina' })]) : page([contact()], { has_more: true, next_cursor: 'CUR1' }) };
    });
    renderAt(<ContactsPage />, '/contacts');
    await screen.findAllByText('Ana Souza');
    await user.click(screen.getByRole('button', { name: /próxima|next/i }));
    await screen.findAllByText('Segunda Pagina');
    expect(listCalls().at(-1)?.cursor).toBe('CUR1');
    await user.selectOptions(screen.getByLabelText('Tipo'), 'customer');
    await screen.findAllByText('Ana Souza');
    expect(listCalls().at(-1)).toMatchObject({ kind: 'customer' });
    expect(listCalls().at(-1)?.cursor).toBeUndefined();
  });

  it('says nothing matched, and "Limpar filtros" brings everything back', async () => {
    const user = userEvent.setup();
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
      if (!(url as string).endsWith('/contacts')) return Promise.reject({ response: { status: 404 } });
      return { data: config?.params?.kind ? page([]) : page([contact()]) };
    });
    renderAt(<ContactsPage />, '/contacts');
    await screen.findAllByText('Ana Souza');
    await user.selectOptions(screen.getByLabelText('Tipo'), 'spam');
    expect(await screen.findByText('Nenhum contato encontrado')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Limpar filtros' }));
    expect((await screen.findAllByText('Ana Souza')).length).toBeGreaterThan(0);
    expect(screen.getByLabelText('Tipo')).toHaveValue('');
  });
});
