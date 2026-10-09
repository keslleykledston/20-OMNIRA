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
  tenant_name: 'Instância ' + id,
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

let candidatesReply: object[] = [];
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
    if (u.endsWith('/transfer-candidates')) return { data: { items: candidatesReply } };
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
      expect(row).toHaveTextContent('Instância ' + id);
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
    expect(screen.getByRole('note')).toHaveTextContent('Somente leitura: seu acesso a ISP Roraima não permite responder.');
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Assumir' })).toBeNull();
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

describe('Navigation — there is no separate "Hub" entry (ADR-0039 revisão: Conversas é a única caixa)', () => {
  const links = () => screen.getAllByRole('link').map((a) => a.getAttribute('href'));

  it.each([['a member of a Hub', undefined], ['no hubs', [] as Hubs], ['the Hub is off (404)', 404 as const]])('the sidebar has no /hub link for %s', async (_n, reply) => {
    if (reply !== undefined) hubsReply = reply;
    serve();
    renderAt(<Sidebar />);
    await waitFor(() => expect(calls().some((u) => u.endsWith('/hubs'))).toBe(true));
    await new Promise((r) => setTimeout(r, 40));
    expect(links()).toContain('/inbox');
    expect(links()).not.toContain('/hub');
  });

  it.each([['a member of a Hub', undefined], ['no hubs', [] as Hubs]])('the mobile bar is unchanged for %s', async (_n, reply) => {
    if (reply !== undefined) hubsReply = reply;
    serve();
    renderAt(<MobileNav />);
    await waitFor(() => expect(calls().some((u) => u.endsWith('/hubs'))).toBe(true));
    await new Promise((r) => setTimeout(r, 40));
    expect(links()).toEqual(['/inbox', '/contacts', '/channels']);
  });
});

describe('HubInboxPage — claiming and replying (write path)', () => {
  const a = item('a', { tenant_name: 'ISP Roraima' });
  const open = async (assignment: 'none' | 'me' | 'other', over: { canReply?: boolean; status?: string } = {}) => {
    pages = { 'hub-1': [{ items: [a], has_more: false }] };
    const base = detail(a);
    details = {
      'hub-1/a': {
        ...base,
        access: { source: 'hub', can_reply: over.canReply ?? true },
        conversation: { ...base.conversation, status: over.status ?? 'open', assignment },
      },
    };
    serve();
    const user = userEvent.setup();
    renderAt(<HubInboxPage />);
    await user.click((await screen.findByText('Cliente a')).closest('button')!);
    await screen.findByText('Sem internet desde ontem');
    return user;
  };
  const posts = () => vi.mocked(axios.post).mock.calls;

  it('"Transferir conversa" is offered only to who holds the conversation, and lists who may receive it with their load', async () => {
    candidatesReply = [{ user_id: 'u2', name: 'Beto', email: 'beto@k3g.com', load: 3 }, { user_id: 'u3', name: '', email: 'cris@k3g.com', load: 0 }];
    const user = await open('me');
    await user.click(screen.getByRole('button', { name: 'Transferir conversa' }));
    const dialog = await screen.findByRole('dialog', { name: 'Transferir conversa' });
    expect(await within(dialog).findByLabelText('Beto')).toBeInTheDocument();
    expect(within(dialog).getByText('3 abertas')).toBeInTheDocument();
    expect(within(dialog).getByRole('button', { name: 'Transferir' })).toBeDisabled(); // nobody picked yet
    const asked = vi.mocked(axios.get).mock.calls.find((c) => String(c[0]).endsWith('/transfer-candidates'))!;
    expect((asked[1] as { params: object }).params).toEqual({ expected_tenant_id: a.tenant_id });
  });

  it.each([['none'], ['other']] as const)('no "Transferir conversa" when the conversation is %s', async (assignment) => {
    await open(assignment);
    expect(screen.queryByRole('button', { name: 'Transferir conversa' })).toBeNull();
  });

  it('transferring sends the chosen person with the DISPLAYED company, then closes the dialog', async () => {
    candidatesReply = [{ user_id: 'u2', name: 'Beto', email: 'beto@k3g.com', load: 3 }];
    vi.mocked(axios.post).mockResolvedValue({ data: { conversation_id: a.conversation_id, assigned_to: 'u2', tenant: { id: a.tenant_id, name: 'ISP Roraima' } } });
    const user = await open('me');
    await user.click(screen.getByRole('button', { name: 'Transferir conversa' }));
    const dialog = await screen.findByRole('dialog', { name: 'Transferir conversa' });
    await user.click(await within(dialog).findByLabelText('Beto'));
    await user.click(within(dialog).getByRole('button', { name: 'Transferir' }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(String(posts()[0][0])).toMatch(/\/hubs\/hub-1\/inbox\/a\/transfer$/);
    expect(posts()[0][1]).toEqual({ expected_tenant_id: a.tenant_id, to_user_id: 'u2' });
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Transferir conversa' })).toBeNull());
  });

  it('giving it back to the queue sends no person', async () => {
    candidatesReply = [];
    vi.mocked(axios.post).mockResolvedValue({ data: { conversation_id: a.conversation_id, assigned_to: null, tenant: { id: a.tenant_id, name: 'ISP Roraima' } } });
    const user = await open('me');
    await user.click(screen.getByRole('button', { name: 'Transferir conversa' }));
    const dialog = await screen.findByRole('dialog', { name: 'Transferir conversa' });
    expect(await within(dialog).findByText(/Ninguém mais tem acesso/)).toBeInTheDocument();
    await user.click(within(dialog).getByRole('button', { name: 'Devolver à fila' }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    expect(posts()[0][1]).toEqual({ expected_tenant_id: a.tenant_id, to_user_id: null });
  });

  it('a person who lost access meanwhile is refused in words and the dialog stays open', async () => {
    candidatesReply = [{ user_id: 'u2', name: 'Beto', email: 'beto@k3g.com', load: 0 }];
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 409, data: 'transfer target not available' } });
    const user = await open('me');
    await user.click(screen.getByRole('button', { name: 'Transferir conversa' }));
    const dialog = await screen.findByRole('dialog', { name: 'Transferir conversa' });
    await user.click(await within(dialog).findByLabelText('Beto'));
    await user.click(within(dialog).getByRole('button', { name: 'Transferir' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/não pode receber esta conversa/);
  });

  it('a read-only grant offers neither claim nor composer', async () => {
    await open('none', { canReply: false });
    expect(screen.queryByRole('button', { name: 'Assumir' })).toBeNull();
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('someone else holds it, or it is finalized: a sentence, no composer, no claim', async () => {
    await open('other');
    expect(screen.getByRole('note')).toHaveTextContent('com outro operador');
    expect(screen.queryByRole('textbox')).toBeNull();
    expect(screen.queryByRole('button', { name: 'Assumir' })).toBeNull();
  });

  it('finalized attendance cannot be answered', async () => {
    await open('me', { status: 'closed' });
    expect(screen.getByRole('note')).toHaveTextContent('finalizado');
    expect(screen.queryByRole('textbox')).toBeNull();
  });

  it('unclaimed: names the company, claims with the displayed company only, then the composer says who it answers as', async () => {
    const user = await open('none');
    expect(screen.getByText(/Assuma a conversa para responder como ISP Roraima/)).toBeInTheDocument();
    expect(screen.queryByRole('textbox')).toBeNull();
    vi.mocked(axios.post).mockImplementation(async () => {
      const d = details['hub-1/a'] as HubItemDetail;
      details['hub-1/a'] = { ...d, conversation: { ...d.conversation, assignment: 'me' } };
      return { data: { item_id: 'a', conversation_id: a.conversation_id, changed: true, tenant: { id: a.tenant_id, name: 'ISP Roraima' } } };
    });
    await user.click(screen.getByRole('button', { name: 'Assumir' }));
    expect(await screen.findByRole('textbox')).toBeInTheDocument();
    expect(screen.getByText('Respondendo como ISP Roraima')).toBeInTheDocument();
    expect(posts()).toHaveLength(1);
    expect(String(posts()[0][0])).toMatch(/\/hubs\/hub-1\/inbox\/a\/claim$/);
    expect(posts()[0][1]).toEqual({ expected_tenant_id: a.tenant_id });
  });

  it('sends text as the displayed company with an Idempotency-Key, and no tenant selector anywhere', async () => {
    const user = await open('me');
    vi.mocked(axios.post).mockResolvedValue({ data: { id: 'x', conversation_id: a.conversation_id, status: 'queued', tenant: { id: a.tenant_id, name: 'ISP Roraima' } } });
    await user.type(await screen.findByRole('textbox'), 'Olá, já verificamos');
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }));
    await waitFor(() => expect(posts()).toHaveLength(1));
    const [url, body, config] = posts()[0] as [string, any, any];
    expect(url).toMatch(/\/hubs\/hub-1\/inbox\/a\/messages$/);
    expect(body).toEqual({ expected_tenant_id: a.tenant_id, text: 'Olá, já verificamos' });
    expect(config.headers['Idempotency-Key']).toMatch(/.{8,}/);
    expect(url).not.toContain('tenant');
  });

  it('a failed send keeps the text, explains why, and a retry of the same text reuses the key (a new text gets a new one)', async () => {
    const user = await open('me');
    vi.mocked(axios.post).mockRejectedValueOnce({ response: { status: 409, data: 'conversation is assigned to another agent' } });
    const box = await screen.findByRole('textbox');
    await user.type(box, 'primeira');
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('Esta conversa já está com outro operador.');
    expect(box).toHaveValue('primeira');
    vi.mocked(axios.post).mockResolvedValue({ data: { id: 'x', conversation_id: a.conversation_id, status: 'queued', tenant: { id: a.tenant_id, name: 'ISP Roraima' } } });
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }));
    await waitFor(() => expect(posts()).toHaveLength(2));
    expect((posts()[1][2] as any).headers['Idempotency-Key']).toBe((posts()[0][2] as any).headers['Idempotency-Key']);
    await user.type(await screen.findByRole('textbox'), 'segunda');
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }));
    await waitFor(() => expect(posts()).toHaveLength(3));
    expect((posts()[2][2] as any).headers['Idempotency-Key']).not.toBe((posts()[0][2] as any).headers['Idempotency-Key']);
  });

  it('a 403 on send says the access is read-only and signs nobody out', async () => {
    const user = await open('me');
    vi.mocked(axios.post).mockRejectedValueOnce({ response: { status: 403, data: 'forbidden' } });
    await user.type(await screen.findByRole('textbox'), 'oi');
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }));
    expect(await screen.findByRole('alert')).toHaveTextContent('somente leitura');
    expect(unauthorized).not.toHaveBeenCalled();
  });
});
