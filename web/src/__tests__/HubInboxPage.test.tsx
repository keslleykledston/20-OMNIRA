import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import HubInboxPage from '../pages/HubInboxPage';
import Sidebar from '../components/Sidebar';
import MobileNav from '../components/MobileNav';
import { renderAt, setSession } from './testUtils';
import type { HubInboxItem, HubItemDetail } from '../lib/hub';

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const item = (id: string, over: Partial<HubInboxItem> = {}): HubInboxItem => ({
  id,
  tenant_id: 'tenant-' + id,
  tenant_name: 'Empresa ' + id,
  conversation_id: 'conv-' + id,
  customer_name: 'Cliente ' + id,
  channel: 'whatsapp',
  status: 'open',
  priority: 'normal',
  unread_count: 0,
  last_activity_at: new Date().toISOString(),
  ...over,
});

const detail = (it: HubInboxItem, over: Partial<HubItemDetail> = {}): HubItemDetail => ({
  item: it,
  tenant: { id: it.tenant_id, name: it.tenant_name },
  access: { source: 'hub' },
  conversation: { id: it.conversation_id, status: it.status, created_at: new Date().toISOString() },
  messages: [
    { id: 'm1', direction: 'inbound', message_type: 'text', body: 'Sem internet desde ontem', status: 'received', created_at: new Date().toISOString() },
    { id: 'm2', direction: 'outbound', message_type: 'text', body: 'Já vamos verificar', status: 'sent', created_at: new Date().toISOString() },
    { id: 'm3', direction: 'inbound', message_type: 'image', body: '', status: 'received', created_at: new Date().toISOString() },
  ],
  ...over,
});

type Hubs = { id: string; name: string; role: string }[];
const H1 = { id: 'hub-1', name: 'K3G Service Desk', role: 'hub_agent' };
const H2 = { id: 'hub-2', name: 'Outro Hub', role: 'hub_agent' };

let hubsReply: Hubs | 404 | 500 = [H1];
let pages: Record<string, { items: HubInboxItem[]; has_more: boolean; next_cursor?: string }[]> = {};
let details: Record<string, HubItemDetail | 404> = {};
let inboxError: number | undefined;

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    const u = url as string;
    if (u.endsWith('/hubs')) {
      if (hubsReply === 404 || hubsReply === 500) return Promise.reject({ response: { status: hubsReply } });
      return { data: { items: hubsReply } };
    }
    let m = u.match(/\/hubs\/([^/]+)\/inbox\/([^/]+)$/);
    if (m) {
      const d = details[`${m[1]}/${m[2]}`];
      if (!d || d === 404) return Promise.reject({ response: { status: 404 } });
      return { data: d };
    }
    m = u.match(/\/hubs\/([^/]+)\/inbox$/);
    if (m) {
      if (inboxError) return Promise.reject({ response: { status: inboxError } });
      const list = pages[m[1]] ?? [{ items: [], has_more: false }];
      const idx = config?.params?.cursor ? Number(String(config.params.cursor).replace('c', '')) : 0;
      const p = list[idx] ?? { items: [], has_more: false };
      return { data: { ...p, count: p.items.length, limit: 30 } };
    }
    if (u.endsWith('/tenants')) return { data: [] };
    return Promise.reject({ response: { status: 404 } });
  });
}

const calls = () => vi.mocked(axios.get).mock.calls.map((c) => String(c[0]));

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  hubsReply = [H1];
  pages = {};
  details = {};
  inboxError = undefined;
});
afterEach(() => {
  delete (window as any).matchMedia;
});

describe('HubInboxPage — availability', () => {
  it('says the Hub is unavailable (and never asks for an inbox) when the server has it off', async () => {
    hubsReply = 404;
    serve();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Hub indisponível')).toBeInTheDocument();
    expect(calls().some((u) => u.includes('/inbox'))).toBe(false);
  });

  it('says the same for someone who belongs to no Hub', async () => {
    hubsReply = [];
    serve();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Hub indisponível')).toBeInTheDocument();
  });

  it('shows an error with a retry when the hubs cannot be loaded, and retry works', async () => {
    hubsReply = 500;
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Não foi possível carregar o Hub.')).toBeInTheDocument();
    hubsReply = [H1];
    pages = { 'hub-1': [{ items: [item('a')], has_more: false }] };
    await user.click(screen.getByRole('button', { name: 'Tentar novamente' }));
    expect(await screen.findByText('Cliente a')).toBeInTheDocument();
  });

  it('signs the user out when the session is no longer valid', async () => {
    inboxError = 401;
    serve();
    renderAt(<HubInboxPage />);
    await waitFor(() => expect(unauthorized).toHaveBeenCalled());
  });
});

describe('HubInboxPage — the aggregated inbox', () => {
  it('lists conversations of several companies, each with the company name in full', async () => {
    pages = { 'hub-1': [{ items: [item('a', { unread_count: 3 }), item('b', { status: 'closed' }), item('c', { priority: 'urgent' })], has_more: false }] };
    serve();
    renderAt(<HubInboxPage />);
    const list = await screen.findByRole('list', { name: 'Conversas do Hub' });
    for (const id of ['a', 'b', 'c']) {
      const row = within(list).getByText('Cliente ' + id).closest('button')!;
      expect(row).toHaveTextContent('Empresa ' + id);
    }
    expect(within(list).getByLabelText('3 sem resposta')).toBeInTheDocument();
    expect(within(list).getByText('Finalizado')).toBeInTheDocument();
    expect(within(list).getByText('Urgente')).toBeInTheDocument();
  });

  it('never sends a tenant selector: the inbox is requested by hub only', async () => {
    pages = { 'hub-1': [{ items: [item('a')], has_more: false }] };
    serve();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    const inboxCalls = vi.mocked(axios.get).mock.calls.filter((c) => String(c[0]).endsWith('/hubs/hub-1/inbox'));
    expect(inboxCalls.length).toBeGreaterThan(0);
    for (const c of inboxCalls) {
      expect(JSON.stringify((c[1] as any)?.params ?? {})).not.toMatch(/tenant/i);
      expect(String(c[0])).not.toMatch(/tenant/i);
    }
  });

  it('shows an empty state when there is nothing delegated', async () => {
    serve();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Nenhuma conversa')).toBeInTheDocument();
  });

  it('shows an error with retry when the inbox fails', async () => {
    inboxError = 500;
    serve();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Não foi possível carregar as conversas.')).toBeInTheDocument();
  });

  it('pages with the server cursor, appends, de-duplicates and hides the button at the end', async () => {
    pages = { 'hub-1': [
      { items: [item('a'), item('b')], has_more: true, next_cursor: 'c1' },
      { items: [item('b'), item('c')], has_more: false },
    ] };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await screen.findByText('Cliente a');
    await user.click(screen.getByRole('button', { name: 'Carregar mais' }));
    await screen.findByText('Cliente c');
    expect(screen.getAllByText('Cliente b')).toHaveLength(1);
    expect(screen.queryByRole('button', { name: 'Carregar mais' })).toBeNull();
    const second = vi.mocked(axios.get).mock.calls.find((c) => (c[1] as any)?.params?.cursor === 'c1');
    expect(second).toBeTruthy();
  });
});

describe('HubInboxPage — opening a conversation', () => {
  it('opens read-only: company in the header, messages, no composer, no media request', async () => {
    const a = item('a', { tenant_name: 'ISP Roraima' });
    pages = { 'hub-1': [{ items: [a], has_more: false }] };
    details = { 'hub-1/a': detail(a) };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    const header = (await screen.findByRole('heading', { name: 'Cliente a' })).closest('header')!;
    expect(header).toHaveTextContent('ISP Roraima');
    expect(await screen.findByText('Sem internet desde ontem')).toBeInTheDocument();
    expect(screen.getByText('Já vamos verificar')).toBeInTheDocument();
    expect(screen.getByRole('note')).toHaveTextContent('Somente leitura');
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.getByText('Imagem (não exibido no Hub)')).toBeInTheDocument();
    // media is fetched by the SESSION tenant elsewhere in the app; here it must never be requested at all
    expect(calls().filter((u) => u.includes('/media') || u.includes('/tenants/'))).toEqual([]);
  });

  it('explains plainly when the conversation can no longer be opened, and goes back to the list', async () => {
    const a = item('a');
    pages = { 'hub-1': [{ items: [a], has_more: false }] };
    details = { 'hub-1/a': 404 };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    expect(await screen.findByText('Não foi possível abrir')).toBeInTheDocument();
    expect(screen.getByText(/acesso pode ter sido encerrado/)).toBeInTheDocument();
    await user.click(screen.getByRole('button', { name: 'Voltar à lista' }));
    expect(await screen.findByText('Selecione uma conversa')).toBeInTheDocument();
  });

  it('marks the selected row and invites a selection before one is made', async () => {
    const a = item('a');
    pages = { 'hub-1': [{ items: [a], has_more: false }] };
    details = { 'hub-1/a': detail(a) };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    expect(await screen.findByText('Selecione uma conversa')).toBeInTheDocument();
    const row = (await screen.findByText('Cliente a')).closest('button')!;
    expect(row).toHaveAttribute('aria-pressed', 'false');
    await user.click(row);
    await waitFor(() => expect(row).toHaveAttribute('aria-pressed', 'true'));
  });

  it('switching hub drops the selection and asks for the other hub only', async () => {
    hubsReply = [H1, H2];
    const a = item('a'), z = item('z');
    pages = { 'hub-1': [{ items: [a], has_more: false }], 'hub-2': [{ items: [z], has_more: false }] };
    details = { 'hub-1/a': detail(a), 'hub-2/z': detail(z) };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    await screen.findByText('Sem internet desde ontem');
    await user.selectOptions(screen.getByLabelText('Trocar de Hub'), 'hub-2');
    expect(await screen.findByText('Cliente z')).toBeInTheDocument();
    expect(screen.queryByText('Cliente a')).toBeNull();
    expect(screen.queryByText('Sem internet desde ontem')).toBeNull();
    expect(screen.getByText('Selecione uma conversa')).toBeInTheDocument();
    expect(calls().some((u) => u.endsWith('/hubs/hub-2/inbox/a'))).toBe(false); // an item id is never carried across hubs
  });
});

describe('HubInboxPage — small screens', () => {
  it('shows the list, then only the conversation with a way back', async () => {
    (window as any).matchMedia = (q: string) => ({ matches: true, media: q, addEventListener() {}, removeEventListener() {} });
    const a = item('a');
    pages = { 'hub-1': [{ items: [a], has_more: false }] };
    details = { 'hub-1/a': detail(a) };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    expect(await screen.findByText('Sem internet desde ontem')).toBeInTheDocument();
    expect(screen.queryByRole('list', { name: 'Conversas do Hub' })).toBeNull();
    await user.click(screen.getByRole('button', { name: 'Voltar para a lista' }));
    expect(await screen.findByRole('list', { name: 'Conversas do Hub' })).toBeInTheDocument();
  });
});

describe('Sidebar — Hub entry', () => {
  const links = () => screen.getAllByRole('link').map((a) => a.getAttribute('href'));

  it('is offered only to people who belong to a Hub, right after Conversas', async () => {
    serve();
    renderAt(<Sidebar />);
    await waitFor(() => expect(calls().some((u) => u.endsWith('/hubs'))).toBe(true));
    expect(links()).toContain('/inbox');
    await waitFor(() => expect(links()).toContain('/hub'));
    const hrefs = links();
    expect(hrefs.indexOf('/hub')).toBe(hrefs.indexOf('/inbox') + 1);
  });

  it.each([['no hubs', [] as Hubs], ['the Hub is off (404)', 404 as const], ['the request fails', 500 as const]])('is absent when %s', async (_n, reply) => {
    hubsReply = reply;
    serve();
    renderAt(<Sidebar />);
    await waitFor(() => expect(calls().some((u) => u.endsWith('/hubs'))).toBe(true));
    await new Promise((r) => setTimeout(r, 40));
    expect(links()).not.toContain('/hub');
  });
});

describe('MobileNav — Hub entry', () => {
  const links = () => screen.getAllByRole('link').map((a) => a.getAttribute('href'));

  it('adds the Hub right after Conversas for members of a Hub', async () => {
    serve();
    renderAt(<MobileNav />);
    await waitFor(() => expect(links()).toContain('/hub'));
    const hrefs = links();
    expect(hrefs.indexOf('/hub')).toBe(hrefs.indexOf('/inbox') + 1);
  });

  it.each([['no hubs', [] as Hubs], ['the Hub is off (404)', 404 as const]])('is unchanged when %s', async (_n, reply) => {
    hubsReply = reply;
    serve();
    renderAt(<MobileNav />);
    await waitFor(() => expect(calls().some((u) => u.endsWith('/hubs'))).toBe(true));
    await new Promise((r) => setTimeout(r, 40));
    expect(links()).toEqual(['/inbox', '/contacts', '/channels']);
  });
});
