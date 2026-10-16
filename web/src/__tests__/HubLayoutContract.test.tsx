import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, fireEvent, render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { MemoryRouter } from 'react-router-dom';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import HubInboxPage from '../pages/HubInboxPage';
import Sidebar from '../components/Sidebar';
import DelegatedContextPane from '../components/inbox/DelegatedContextPane';
import { renderAt, setSession } from './testUtils';
import { clearActing, getActingHub, setActing } from '../lib/acting';
import type { HubInboxItem, HubItemDetail } from '../lib/hub';

// THE APPROVED LAYOUT OF THE HUB INBOX (docs/ux/HUB-LAYOUT-CONTRACT.md). These tests pin its structure, not its pixels: the queue of pending
// attendances apart from the history, the conversation, and the collapsible details of the attendance beside it; the company always named;
// the left menu foldable. A later phase (a new tenant-division rule, a new key, a new data source) may change what fills these regions, but if a
// change makes one of these tests fail it has changed the approved layout and needs the owner's decision, not a quiet edit of the test.
vi.mock('axios');

const item = (id: string, over: Partial<HubInboxItem> = {}): HubInboxItem => ({
  id,
  tenant_id: 'tenant-' + id,
  tenant_name: 'Instância ' + id,
  conversation_id: 'conv-' + id,
  customer_name: 'Cliente ' + id,
  channel: 'whatsapp',
  status: 'open',
  priority: 'normal',
  unread_count: 0,
  assignment: 'none',
  last_activity_at: new Date().toISOString(),
  ...over,
});

const detail = (it: HubInboxItem, assignment: 'none' | 'me' | 'other' = 'none'): HubItemDetail => ({
  item: it,
  tenant: { id: it.tenant_id, name: it.tenant_name },
  access: { source: 'hub', can_reply: true },
  conversation: { id: it.conversation_id, status: it.status, created_at: '2026-10-01T10:00:00Z', assignment },
  messages: [
    { id: 'm1', direction: 'inbound', message_type: 'text', body: 'Sem internet desde ontem', status: 'received', created_at: '2026-10-01T10:00:00Z' },
    { id: 'm2', direction: 'outbound', message_type: 'text', body: 'Já vamos verificar', status: 'sent', created_at: '2026-10-01T10:05:00Z' },
  ],
});

const H1 = { id: 'hub-1', name: 'K3G Service Desk', role: 'hub_agent' };
let items: HubInboxItem[] = [];
let fullContext = true;
let counts: { open: number; waiting: number; mine: number; closed: number } | undefined;
const inboxParams: Record<string, string>[] = [];

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    const u = String(url);
    if (u.endsWith('/hubs')) return { data: { items: [H1] } };
    let m = u.match(/\/hubs\/([^/]+)\/inbox\/([^/]+)$/);
    if (m) {
      const it = items.find((i) => i.id === m![2]);
      return it ? { data: detail(it, it.assignment ?? 'none') } : Promise.reject({ response: { status: 404 } });
    }
    m = u.match(/\/hubs\/([^/]+)\/inbox$/);
    if (m) {
      inboxParams.push({ ...(config?.params ?? {}) });
      const seg = config?.params?.segment as string | undefined;
      const q = String(config?.params?.q ?? '').toLowerCase();
      const shown = items.filter(
        (i) =>
          (!seg || (seg === 'closed' ? i.status === 'closed' : i.status !== 'closed')) &&
          (seg !== 'waiting' || i.assignment === 'none') &&
          (seg !== 'mine' || i.assignment === 'me') &&
          i.customer_name.toLowerCase().includes(q),
      );
      const companies = items.map((i) => ({ id: i.tenant_id, name: i.tenant_name, full_context: fullContext }));
      return { data: { items: shown, has_more: false, count: shown.length, limit: 30, companies, ...(counts && !config?.params?.cursor ? { counts } : {}) } };
    }
    if (u.endsWith('/tenants')) return { data: [] };
    return Promise.reject({ response: { status: 404 } });
  });
}

const lastSegment = () => inboxParams[inboxParams.length - 1]?.segment;
const narrow = () => {
  // a tablet: not a phone (the list and the conversation sit side by side) but under 1440px (the details open in a drawer)
  (window as any).matchMedia = (q: string) => ({ matches: false, media: q, addEventListener() {}, removeEventListener() {} });
};

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  clearActing();
  setSession();
  items = [item('a', { assignment: 'none' }), item('b', { assignment: 'me', unread_count: 2 }), item('c', { assignment: 'other' }), item('d', { status: 'closed' })];
  fullContext = true;
  counts = undefined;
  inboxParams.length = 0;
  delete (window as any).matchMedia;
});
afterEach(() => {
  clearActing();
  delete (window as any).matchMedia;
});

describe('Hub layout contract — the queue is apart from the history', () => {
  it('has the scope tabs (in progress / finalized), the filter tabs, the search and the company filter in the queue column', async () => {
    serve();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    const queue = screen.getByRole('region', { name: 'Caixa do Hub' });
    const scope = within(queue).getByRole('tablist', { name: 'Escopo das conversas' });
    expect(within(scope).getAllByRole('tab').map((t) => t.textContent)).toEqual(['Em andamento', 'Encerradas']);
    const filters = within(queue).getByRole('tablist', { name: 'Filtro de conversas' });
    expect(within(filters).getAllByRole('tab').map((t) => t.textContent?.replace(/\s*\(\d+\)/, ''))).toEqual(['Todas', 'Pendentes', 'Minhas']);
    expect(within(queue).getByRole('searchbox', { name: 'Buscar cliente' })).toBeInTheDocument();
  });

  it('asks the server for the in-progress queue by default, the pending ones, mine and, apart, the history', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    expect(lastSegment()).toBe('open');
    expect(screen.queryByText('Cliente d')).toBeNull(); // the finalized one is not in the queue

    await user.click(screen.getByRole('tab', { name: /Pendentes/ }));
    await waitFor(() => expect(lastSegment()).toBe('waiting'));
    expect(await screen.findByText('Cliente a')).toBeInTheDocument();
    expect(screen.queryByText('Cliente b')).toBeNull();

    await user.click(screen.getByRole('tab', { name: /Minhas/ }));
    await waitFor(() => expect(lastSegment()).toBe('mine'));
    expect(await screen.findByText('Cliente b')).toBeInTheDocument();

    await user.click(screen.getByRole('tab', { name: 'Encerradas' }));
    await waitFor(() => expect(lastSegment()).toBe('closed'));
    expect(await screen.findByText('Cliente d')).toBeInTheDocument();
    expect(screen.queryByRole('tablist', { name: 'Filtro de conversas' })).toBeNull(); // the history has no queue filter

    await user.click(screen.getByRole('tab', { name: 'Em andamento' }));
    await waitFor(() => expect(lastSegment()).toBe('open'));
    expect(await screen.findByText('Cliente a')).toBeInTheDocument();
  });

  it('the search is sent after a pause, trimmed, as q (never as a tenant)', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    await user.type(screen.getByRole('searchbox', { name: 'Buscar cliente' }), '  cliente b ');
    await waitFor(() => expect(inboxParams.some((p) => p.q === 'cliente b')).toBe(true));
    expect(inboxParams.every((p) => !('tenant_id' in p))).toBe(true);
    expect(await screen.findByText('Cliente b')).toBeInTheDocument();
    expect(screen.queryByText('Cliente a')).toBeNull();
    await user.clear(screen.getByRole('searchbox', { name: 'Buscar cliente' }));
    await user.type(screen.getByRole('searchbox', { name: 'Buscar cliente' }), 'ninguem');
    expect(await screen.findByText('Nada encontrado para esta busca.')).toBeInTheDocument();
  });

  it('asks for the counters of the tabs (they are opt-in on the server) and for nothing else it does not show', async () => {
    serve();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    expect(inboxParams.every((p) => p.include === 'counts')).toBe(true);
  });

  it('shows the counters of the tabs (the pending queue first) and a plain empty queue', async () => {
    counts = { open: 3, waiting: 2, mine: 1, closed: 1 };
    serve();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    expect(await screen.findByRole('tab', { name: 'Pendentes (2)' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Minhas (1)' })).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Todas (3)' })).toBeInTheDocument();
  });

  it('each row names the company in full and who holds it, relative to the caller', async () => {
    serve();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    const list = screen.getByRole('list', { name: 'Conversas do Hub' });
    const row = (n: string) => within(list).getByText('Cliente ' + n).closest('button')!;
    expect(row('a')).toHaveTextContent('Instância a');
    expect(row('a')).toHaveTextContent('Na fila');
    expect(row('b')).toHaveTextContent('Com você');
    expect(row('c')).toHaveTextContent('Com outro');
    expect(row('b')).toHaveAttribute('aria-pressed', 'false');
  });
});

describe('Hub layout contract — queue | conversation | collapsible details', () => {
  const openA = async () => {
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    await screen.findByText('Sem internet desde ontem');
    return user;
  };

  it('with room, the details of the attendance sit beside the conversation, in sections that fold', async () => {
    serve();
    await openA();
    const aside = await screen.findByRole('complementary', { name: 'Detalhes do atendimento' });
    // the state of the attendance is a two-line strip (always visible); the rest folds
    expect(within(aside).getAllByText(/^(Contato|Linha do tempo|Ações)$/).map((e) => e.textContent)).toEqual(['Contato', 'Linha do tempo', 'Ações']);
    expect(within(aside).getByLabelText('Atendimento')).toHaveTextContent('Em atendimento');
    expect(aside.querySelectorAll('details')).toHaveLength(3);
    expect(aside).toHaveTextContent('Instância a'); // the company, in full
    expect(aside).toHaveTextContent('Sem responsável');
    // the three regions are siblings of one grid, in this order
    const regions = [screen.getByRole('region', { name: 'Caixa do Hub' }), screen.getByRole('region', { name: 'Conversa selecionada' }), aside.parentElement!];
    const grid = regions[0].parentElement!;
    expect(regions.every((r) => r.parentElement === grid)).toBe(true);
    expect(Array.from(grid.children)).toEqual(regions);
  });

  it('a section folds, and the choice is remembered for the viewer', async () => {
    serve();
    await openA();
    const aside = await screen.findByRole('complementary', { name: 'Detalhes do atendimento' });
    const section = within(aside).getByText('Linha do tempo').closest('details')!;
    expect(section.open).toBe(true);
    fireEvent.click(within(section).getByText('Linha do tempo'));
    section.open = false;
    fireEvent(section, new Event('toggle'));
    expect(localStorage.getItem('omnira.inbox.section.hub-timeline')).toBe('0');
  });

  it('the details button hides and shows the column; the conversation keeps working', async () => {
    serve();
    const user = await openA();
    expect(await screen.findByRole('complementary', { name: 'Detalhes do atendimento' })).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Detalhes do atendimento' }));
    expect(screen.queryByRole('complementary', { name: 'Detalhes do atendimento' })).toBeNull();
    expect(screen.getByText('Sem internet desde ontem')).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Detalhes do atendimento' }));
    expect(await screen.findByRole('complementary', { name: 'Detalhes do atendimento' })).toBeInTheDocument();
  });

  it('under 1440px the details are not squeezed in: they open in a drawer from the same button', async () => {
    narrow();
    serve();
    const user = await openA();
    expect(screen.queryByRole('complementary', { name: 'Detalhes do atendimento' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Detalhes do atendimento' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByRole('complementary', { name: 'Detalhes do atendimento' })).toBeInTheDocument();
  });

  it('the full workspace of the company is one click away only when the server said so, and the click only navigates', async () => {
    const assign = vi.fn();
    const original = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, assign } });
    try {
      serve();
      const user = await openA();
      const aside = await screen.findByRole('complementary', { name: 'Detalhes do atendimento' });
      await user.click(within(aside).getByRole('button', { name: /Abrir na caixa completa de Instância a/ }));
      expect(assign).toHaveBeenCalledWith('/inbox?instancia=tenant-a&conversation_id=conv-a');
      expect(getActingHub()).toBe('hub-1'); // entered as an agent of THIS hub; nothing else was decided by the screen
    } finally {
      Object.defineProperty(window, 'location', { configurable: true, value: original });
    }
  });

  it('without the full context the pane says so instead of offering a button that would be refused', async () => {
    fullContext = false;
    serve();
    await openA();
    const aside = await screen.findByRole('complementary', { name: 'Detalhes do atendimento' });
    expect(within(aside).queryByRole('button', { name: /Abrir na caixa completa/ })).toBeNull();
    expect(aside).toHaveTextContent('não está liberada para você');
  });
});

describe('Hub layout contract — which company is being served, and the attendances opened as tabs', () => {
  const rowOf = async (n: string) => (await screen.findByText('Cliente ' + n)).closest('button')!;

  it('says above the panes which company the person is acting for (the whole box before a conversation is open)', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    expect(screen.getByText('Caixa: Todas as instâncias')).toBeInTheDocument();
    expect(screen.getByText('3 conversas')).toBeInTheDocument(); // the three open ones
    await user.click(await rowOf('a'));
    expect(await screen.findByText('Contexto ativo: Instância a')).toBeInTheDocument();
    await user.click(await rowOf('b'));
    expect(await screen.findByText('Contexto ativo: Instância b')).toBeInTheDocument();
  });

  it('opening conversations keeps them as tabs above the conversation, each with its company; a tab selects, closing only forgets it', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    expect(screen.queryByRole('tablist', { name: 'Atendimentos abertos' })).toBeNull();
    await user.click(await rowOf('a'));
    await user.click(await rowOf('b'));
    const strip = await screen.findByRole('tablist', { name: 'Atendimentos abertos' });
    const tabs = within(strip).getAllByRole('tab');
    expect(tabs.map((t) => t.textContent)).toEqual(['CACliente aInstância a', 'CBCliente bInstância b']);
    expect(tabs[1]).toHaveAttribute('aria-selected', 'true');
    // another tab brings its conversation back
    await user.click(tabs[0]);
    expect(await screen.findByRole('heading', { name: 'Cliente a' })).toBeInTheDocument();
    expect(within(strip).getAllByRole('tab')[0]).toHaveAttribute('aria-selected', 'true');
    // closing the open one selects its neighbour and touches nothing on the server
    const writes = vi.mocked(axios.post).mock.calls.length;
    await user.click(within(strip).getByRole('button', { name: 'Fechar aba de Cliente a' }));
    expect(await screen.findByRole('heading', { name: 'Cliente b' })).toBeInTheDocument();
    expect(within(screen.getByRole('tablist', { name: 'Atendimentos abertos' })).getAllByRole('tab')).toHaveLength(1);
    expect(vi.mocked(axios.post).mock.calls.length).toBe(writes);
    // closing the last one leaves the invitation to pick a conversation
    await user.click(screen.getByRole('button', { name: 'Fechar aba de Cliente b' }));
    expect(await screen.findByText('Selecione uma conversa')).toBeInTheDocument();
    expect(screen.queryByRole('tablist', { name: 'Atendimentos abertos' })).toBeNull();
  });

  it('taking an attendance asks the list again, so it leaves the pending queue and is in "Minhas" without waiting for the next poll', async () => {
    serve();
    vi.mocked(axios.post).mockResolvedValue({ data: { item_id: 'a', conversation_id: 'conv-a', changed: true, tenant: { id: 'tenant-a', name: 'Instância a' } } });
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click(await rowOf('a'));
    await screen.findByRole('button', { name: 'Assumir' });
    const before = inboxParams.length;
    await user.click(screen.getByRole('button', { name: 'Assumir' }));
    await waitFor(() => expect(inboxParams.length).toBeGreaterThan(before));
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/\/inbox\/a\/claim$/);
  });

  it('a company the server stops offering takes its tabs (and the names in them) away', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click(await rowOf('a'));
    await user.click(await rowOf('b'));
    const strip = await screen.findByRole('tablist', { name: 'Atendimentos abertos' });
    expect(within(strip).getAllByRole('tab')).toHaveLength(2);
    items = items.filter((i) => i.tenant_id !== 'tenant-b'); // the grant on company b ended
    await user.click(screen.getByRole('tab', { name: /Minhas/ })); // any new read of the list brings the companies the person may serve now
    await waitFor(() => expect(within(screen.getByRole('tablist', { name: 'Atendimentos abertos' })).getAllByRole('tab')).toHaveLength(1));
    expect(screen.queryByText('Cliente b', { selector: 'strong, span' })).toBeNull();
    // and the attendance that was open on screen goes with it: no cached messages of a company the person may no longer read
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Cliente b' })).toBeNull());
    expect(screen.queryByText('Sem internet desde ontem')).toBeNull();
    expect(screen.getByText('Selecione uma conversa')).toBeInTheDocument();
  });

  it('a conversation that can no longer be opened does not stay as a tab', async () => {
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click(await rowOf('a'));
    await screen.findByRole('tablist', { name: 'Atendimentos abertos' });
    items = items.filter((i) => i.id !== 'b'); // access to b ended: its detail is a 404
    const origGet = vi.mocked(axios.get).getMockImplementation()!;
    vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => (String(url).endsWith('/inbox/b') ? Promise.reject({ response: { status: 404 } }) : origGet(url, config)));
    await user.click(await rowOf('c'));
    await screen.findByRole('heading', { name: 'Cliente c' });
    items = [item('a', { assignment: 'none' }), item('b', { assignment: 'me' }), item('c', { assignment: 'other' })];
    await user.click(screen.getByRole('button', { name: 'Fechar aba de Cliente c' }));
    expect(await screen.findByRole('heading', { name: 'Cliente a' })).toBeInTheDocument();
  });
});

describe('Hub layout contract — the attendances opened as tabs survive a reload, as ids only', () => {
  const rowOf = async (n: string) => (await screen.findByText('Cliente ' + n)).closest('button')!;
  const tabNames = () => within(screen.getByRole('tablist', { name: 'Atendimentos abertos' })).getAllByRole('tab').map((t) => t.textContent);

  it('brings the open attendances and the selected one back after a reload, with names the SERVER says now; the browser keeps ids only', async () => {
    serve();
    const user = userEvent.setup();
    const first = renderAt(<HubInboxPage />);
    await user.click(await rowOf('a'));
    await user.click(await rowOf('b'));
    await screen.findByRole('tablist', { name: 'Atendimentos abertos' });
    await waitFor(() => expect(sessionStorage.getItem('omnira.hub.tabs.hub-1')).not.toBeNull());
    const kept = sessionStorage.getItem('omnira.hub.tabs.hub-1')!;
    expect(JSON.parse(kept)).toEqual({ ids: ['a', 'b'], selected: 'b' });
    expect(kept).not.toMatch(/Cliente|Instância/); // no name of a customer or a company is written to the browser
    first.unmount();
    // the reload: a customer was renamed meanwhile; the tab shows what the server says now
    items = items.map((i) => (i.id === 'a' ? { ...i, customer_name: 'Cliente a (renomeado)' } : i));
    renderAt(<HubInboxPage />);
    await waitFor(() => expect(tabNames()).toEqual(['CACliente a (renomeado)Instância a', 'CBCliente bInstância b']));
    expect(await screen.findByRole('heading', { name: 'Cliente b' })).toBeInTheDocument();
  });

  it('does not bring back an attendance the person can no longer open', async () => {
    serve();
    const user = userEvent.setup();
    const first = renderAt(<HubInboxPage />);
    await user.click(await rowOf('a'));
    await user.click(await rowOf('b'));
    await waitFor(() => expect(JSON.parse(sessionStorage.getItem('omnira.hub.tabs.hub-1') ?? '{}').ids).toEqual(['a', 'b']));
    first.unmount();
    items = items.filter((i) => i.id !== 'a'); // the access to a ended: its detail is a 404
    renderAt(<HubInboxPage />);
    await waitFor(() => expect(tabNames()).toEqual(['CBCliente bInstância b']));
    await waitFor(() => expect(JSON.parse(sessionStorage.getItem('omnira.hub.tabs.hub-1') ?? '{}').ids).toEqual(['b']));
  });

  it('does not overwrite what was kept with the empty first render, and a sign-out forgets it', async () => {
    serve();
    sessionStorage.setItem('omnira.hub.tabs.hub-1', JSON.stringify({ ids: ['a'], selected: 'a' }));
    renderAt(<HubInboxPage />);
    await waitFor(() => expect(tabNames()).toEqual(['CACliente aInstância a']));
    const { clearSession } = await import('../lib/session');
    clearSession();
    expect(sessionStorage.getItem('omnira.hub.tabs.hub-1')).toBeNull();
  });

  it('ignores a damaged or oversized entry instead of failing', async () => {
    serve();
    sessionStorage.setItem('omnira.hub.tabs.hub-1', '{not json');
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    expect(screen.queryByRole('tablist', { name: 'Atendimentos abertos' })).toBeNull();
  });
});

describe('Hub layout contract — the aggregated tab follows the change signal of the Hub', () => {
  it('re-reads the list as soon as the Hub says something changed, without waiting for the next poll', async () => {
    serve();
    let push!: (t: string) => void;
    const body = new ReadableStream<Uint8Array>({ start: (c) => (push = (t) => c.enqueue(new TextEncoder().encode(t))) });
    const fetchMock = vi.fn().mockResolvedValue({ status: 200, ok: true, body } as unknown as Response);
    vi.stubGlobal('fetch', fetchMock);
    try {
      renderAt(<HubInboxPage />);
      await screen.findByText('Cliente a');
      await waitFor(() => expect(fetchMock).toHaveBeenCalled());
      expect(String(fetchMock.mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/events$/);
      const before = inboxParams.length;
      push('event: changed\ndata: {}\n\n');
      await waitFor(() => expect(inboxParams.length).toBeGreaterThan(before));
    } finally {
      vi.unstubAllGlobals();
    }
  });
});

describe('Hub layout contract — the left menu folds to icons', () => {
  it('has a toggle that folds and unfolds it, and remembers the choice', async () => {
    serve();
    const first = renderAt(<Sidebar />);
    const toggle = await screen.findByRole('button', { name: 'Recolher o menu' });
    expect(toggle).toHaveAttribute('aria-expanded', 'true');
    const label = () => screen.getAllByText('Conversas').find((e) => e.tagName === 'SPAN')!;
    expect(label().className).toContain('xl:inline');
    await userEvent.click(toggle);
    expect(screen.getByRole('button', { name: 'Expandir o menu' })).toHaveAttribute('aria-expanded', 'false');
    expect(label().className).not.toContain('xl:inline');
    expect(localStorage.getItem('omnira.sidebar.collapsed')).toBe('1');
    first.unmount();
    renderAt(<Sidebar />);
    expect(await screen.findByRole('button', { name: 'Expandir o menu' })).toBeInTheDocument(); // remembered
    await userEvent.click(screen.getByRole('button', { name: 'Expandir o menu' }));
    expect(localStorage.getItem('omnira.sidebar.collapsed')).toBe('0');
  });
});

describe('Hub layout contract — the company workspace entered through the Hub keeps the same collapsible sections', () => {
  it('the delegated details pane is made of the same foldable sections as the member pane', async () => {
    const get = vi.mocked(axios.get);
    get.mockImplementation(async (url: string) => {
      const u = String(url);
      if (u.endsWith('/me/access')) return { data: { role_key: 'hub_delegate', permissions: ['conversation.read', 'contact.read', 'attendance.history'] } };
      if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', status: 'open', contact_id: 'k1', assigned_to_user_id: 'u-9' } };
      if (u.endsWith('/contacts/k1')) return { data: { id: 'k1', display_name: 'Berjon Brito', kind: 'customer' } };
      return Promise.reject({ response: { status: 404 } });
    });
    setActing('hub-1', 'Beta Hub');
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter>
          <DelegatedContextPane conversationId="c1" />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    const aside = await screen.findByRole('complementary', { name: 'Detalhes do atendimento' });
    await waitFor(() => expect(aside.querySelectorAll('details').length).toBeGreaterThanOrEqual(3));
    const titles = Array.from(aside.querySelectorAll('details > summary')).map((s) => s.textContent?.trim());
    expect(titles).toEqual(expect.arrayContaining(['Contato', 'Atendimento', 'Histórico do contato']));
    // nothing a delegated agent could already reach is hidden behind a click: every section starts open
    expect(Array.from(aside.querySelectorAll('details')).every((d) => d.open)).toBe(true);
    act(() => clearActing());
  });
});
