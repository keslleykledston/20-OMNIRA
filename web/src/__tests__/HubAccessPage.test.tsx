import { useState } from 'react';
import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import HubAccessPage, { instanceLabel } from '../pages/HubAccessPage';
import CompanyFilter, { filterSummary } from '../components/hub/CompanyFilter';
import ConversationsEntry from '../pages/ConversationsEntry';
import { render } from '@testing-library/react';
import { renderAt, setSession } from './testUtils';
import type { AccessOverview } from '../lib/hub';

vi.mock('axios');
vi.mock('../pages/InboxWorkspace', () => ({ default: () => <div>Caixa completa da instância</div> }));
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const person = (id: string, email: string, name = '') => ({ user_id: id, email, name });
const overview = (): AccessOverview => ({
  hub_id: 'hub-1', hub_name: 'K3G Solutions',
  instances: [
    { tenant_id: 'A', name: 'Alfa', tenant_status: 'active', contract_status: 'active', admins: [person('u-adm', 'adm@alfa.com', 'Ana Admin')], direct_agents: 2, hub_agents: 1 },
    { tenant_id: 'B', name: 'Beta', tenant_status: 'suspended', contract_status: 'active', admins: [], direct_agents: 0, hub_agents: 0 },
    { tenant_id: 'C', name: 'Gama', tenant_status: 'active', contract_status: 'active', admins: [person('u1', 'um@gama.com'), person('u2', 'dois@gama.com')], direct_agents: 1, hub_agents: 0 },
  ],
  agents: [
    { ...person('u-hub', 'chefe@k3g.com', 'Chefe'), hub_role: 'hub_admin', grants: [], direct_instances: [], instances: 0 },
    { ...person('u-x', 'x@k3g.com', 'Xavier'), hub_role: 'hub_agent', grants: [{ tenant_id: 'A', mode: 'reply' }, { tenant_id: 'C', mode: 'read' }], direct_instances: [], instances: 2 },
    { ...person('u-y', 'y@k3g.com'), hub_role: 'hub_agent', grants: [{ tenant_id: 'A', mode: 'read' }], direct_instances: ['B'], instances: 2 },
  ],
});

let hubs: object[] = [];
let ov: AccessOverview;

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
    if (String(url).endsWith('/access')) return { data: ov };
    if (String(url).endsWith('/companies')) return { data: { items: ov.instances.map((i) => ({ id: i.tenant_id, legal_name: i.name, trade_name: i.name, display_name: i.name, status: i.tenant_status, contract_status: i.contract_status, capabilities: {}, channels: 0, integrations: 0, open_conversations: 0, agents: 0, created_at: new Date().toISOString() })), capabilities: [] } };
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_admin', can_manage_access: true, can_manage_companies: true }];
  ov = overview();
  serve();
});

describe('HubAccessPage', () => {
  it('is unavailable (and asks for nothing) for someone who is not an admin of a hub', async () => {
    hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_agent', can_manage_access: false }];
    renderAt(<HubAccessPage />);
    expect(await screen.findByText('Painel de acessos indisponível')).toBeInTheDocument();
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).includes('/access'))).toBe(false);
  });

  it('shows who works in one or in several instances, and what each cell allows', async () => {
    renderAt(<HubAccessPage />);
    const table = await screen.findByRole('table');
    expect(within(table).getByLabelText('Acesso de x@k3g.com em Alfa')).toHaveValue('reply');
    expect(within(table).getByLabelText('Acesso de x@k3g.com em Gama')).toHaveValue('read');
    expect(within(table).getByLabelText('Acesso de x@k3g.com em Beta')).toHaveValue('none');
    const rowX = within(table).getByText('Xavier').closest('tr')!;
    expect(within(rowX).getByText('2 instâncias')).toBeInTheDocument();
    // direct membership counts and is named: nobody hides a second instance
    const rowY = within(table).getByText('y@k3g.com', { selector: 'div' }).closest('tr')!;
    expect(within(rowY).getByText(/Também é membro direto de: Beta/)).toBeInTheDocument();
    expect(within(table).getByText('Admin do Hub').closest('tr')).toBe(within(table).getByText('Chefe').closest('tr'));
    // a suspended instance cannot receive access
    expect(within(table).getByLabelText('Acesso de x@k3g.com em Beta')).toBeDisabled();
    expect(within(table).getByText('Suspensa')).toBeInTheDocument();
  });

  it('changing a cell sends exactly that agent, that instance and that mode', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: {} });
    renderAt(<HubAccessPage />);
    const sel = await screen.findByLabelText('Acesso de y@k3g.com em Gama');
    await userEvent.selectOptions(sel, 'reply');
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1));
    const [url, body] = vi.mocked(axios.put).mock.calls[0];
    expect(String(url)).toMatch(/\/hubs\/hub-1\/access\/agents\/u-y\/instances\/C$/);
    expect(body).toEqual({ mode: 'reply', valid_until: null });
  });

  it('a server refusal is said in words and the table is reloaded', async () => {
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 404 } });
    renderAt(<HubAccessPage />);
    await userEvent.selectOptions(await screen.findByLabelText('Acesso de y@k3g.com em Gama'), 'read');
    expect(await screen.findByRole('alert')).toHaveTextContent(/não administra este Hub/);
  });

  it('adding a person who already has an account sends the e-mail with the chosen access, and says it applied', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { status: 'applied' } });
    renderAt(<HubAccessPage />);
    await userEvent.type(await screen.findByLabelText('Adicionar pessoa ao Hub'), 'novo@k3g.com');
    await userEvent.selectOptions(screen.getByLabelText('Acesso inicial em Alfa'), 'reply');
    await userEvent.selectOptions(screen.getByLabelText('Acesso inicial em Gama'), 'read');
    // a suspended instance is not offered
    expect(screen.queryByLabelText('Acesso inicial em Beta')).toBeNull();
    await userEvent.click(screen.getByRole('button', { name: 'Adicionar' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/access\/invitations$/);
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({
      email: 'novo@k3g.com',
      access: [{ tenant_id: 'A', mode: 'reply' }, { tenant_id: 'C', mode: 'read' }],
    });
    expect(await screen.findByRole('alert')).toHaveTextContent(/já tinha conta.*com o acesso escolhido/);
  });

  it('adding a person with no access picked sends an empty list; a person without an account is told it waits for the first sign-in', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { status: 'pending', expires_at: '2026-10-23T00:00:00Z' } });
    renderAt(<HubAccessPage />);
    await userEvent.type(await screen.findByLabelText('Adicionar pessoa ao Hub'), 'futuro@k3g.com');
    await userEvent.click(screen.getByRole('button', { name: 'Adicionar' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ email: 'futuro@k3g.com', access: [] });
    expect(await screen.findByRole('alert')).toHaveTextContent(/ainda não tem conta.*primeiro acesso/);
  });

  it('lists the authorizations that wait for a first sign-in and cancels one', async () => {
    ov = { ...overview(), invitations: [{ id: 'inv-1', email: 'futuro@k3g.com', access: [{ tenant_id: 'A', mode: 'reply' }], created_at: '2026-10-09T00:00:00Z', expires_at: '2026-10-23T00:00:00Z' }] };
    vi.mocked(axios.delete).mockResolvedValue({ data: {} });
    renderAt(<HubAccessPage />);
    const box = await screen.findByRole('region', { name: 'Autorizações aguardando o primeiro acesso' });
    expect(within(box).getByText('futuro@k3g.com')).toBeInTheDocument();
    expect(within(box).getByText(/Alfa: Ler e responder/)).toBeInTheDocument();
    await userEvent.click(within(box).getByRole('button', { name: 'Cancelar a autorização de futuro@k3g.com' }));
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/access\/invitations\/inv-1$/);
  });

  it('removing an agent asks first; cancelling changes nothing', async () => {
    renderAt(<HubAccessPage />);
    const rowX = (await screen.findByText('Xavier')).closest('tr')!;
    await userEvent.click(within(rowX).getByRole('button', { name: 'Remover do Hub' }));
    expect(await screen.findByText(/acessos deste agente às instâncias deste Hub terminam na hora/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Cancelar' }));
    expect(axios.delete).not.toHaveBeenCalled();
    // hub admins have no "remove" here: that is hubctl's
    const rowAdmin = screen.getByText('Chefe').closest('tr')!;
    expect(within(rowAdmin).queryByRole('button', { name: 'Remover do Hub' })).toBeNull();
  });

  it('one tab, one list: the instances tab shows each instance with its administrators when the person can manage instances', async () => {
    renderAt(<HubAccessPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Instâncias' }));
    const alfa = await screen.findByRole('region', { name: 'Instância Alfa' });
    expect(within(alfa).getByText('Ana Admin')).toBeInTheDocument();
    expect(within(alfa).getByRole('button', { name: 'Suspender' })).toBeInTheDocument();
    expect(screen.getByRole('button', { name: 'Nova instância' })).toBeInTheDocument();
    // the old second screen is gone: no link to a separate "Empresas"/"Instâncias" page
    expect(screen.queryByRole('link', { name: /Empresas|Instâncias/ })).toBeNull();
  });

  it('the instances tab lists administrators, adds one and asks before withdrawing one', async () => {
    hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_admin', can_manage_access: true, can_manage_companies: false }];
    vi.mocked(axios.post).mockResolvedValue({ data: person('u-n', 'novo@alfa.com') });
    vi.mocked(axios.delete).mockResolvedValue({ data: {} });
    renderAt(<HubAccessPage />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Instâncias' }));
    const alfa = await screen.findByRole('article', { name: 'Instância Alfa' });
    expect(within(alfa).getByText('Ana Admin')).toBeInTheDocument();
    expect(screen.getByRole('article', { name: 'Instância Beta' })).toHaveTextContent('Sem administrador');
    // a suspended instance takes no new administrator from here
    expect(within(screen.getByRole('article', { name: 'Instância Beta' })).queryByLabelText(/Novo administrador/)).toBeNull();

    await userEvent.type(within(alfa).getByLabelText('Novo administrador de Alfa'), 'novo@alfa.com');
    await userEvent.click(within(alfa).getByRole('button', { name: 'Adicionar' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/access\/instances\/A\/admins$/);

    const gama = screen.getByRole('article', { name: 'Instância Gama' });
    await userEvent.click(within(gama).getByRole('button', { name: 'Retirar administração de dois@gama.com em Gama' }));
    expect(await screen.findByText(/continua na instância como atendente/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Retirar administração' }));
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/access\/instances\/C\/admins\/u2$/);
  });

  it('with two hubs administered, the admin picks which one the panel shows', async () => {
    hubs = [
      { id: 'hub-1', name: 'Primeiro', role: 'hub_admin', can_manage_access: true },
      { id: 'hub-2', name: 'Segundo', role: 'hub_admin', can_manage_access: true },
    ];
    renderAt(<HubAccessPage />);
    await screen.findByRole('table');
    await userEvent.selectOptions(screen.getByLabelText('Trocar de Hub'), 'hub-2');
    await waitFor(() => expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).endsWith('/hubs/hub-2/access'))).toBe(true));
  });

  it('labels', () => {
    expect(instanceLabel(0)).toBe('Nenhuma instância');
    expect(instanceLabel(1)).toBe('Uma instância');
    expect(instanceLabel(3)).toBe('3 instâncias');
  });
});

describe('CompanyFilter', () => {
  const cos = [{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }, { id: 'C', name: 'Gama' }];

  it('summarises: all by default, one by name, several by count', () => {
    expect(filterSummary(cos, [])).toBe('Todas as instâncias');
    expect(filterSummary(cos, ['B'])).toBe('Beta');
    expect(filterSummary(cos, ['A', 'C'])).toBe('2 instâncias');
    expect(filterSummary(cos, ['A', 'B', 'C'])).toBe('Todas as instâncias');
    expect(filterSummary(cos, ['zzz'])).toBe('Todas as instâncias'); // an id the person no longer serves narrows nothing
  });

  it('picks one, then more, and goes back to all', async () => {
    const calls: string[][] = [];
    function Host() {
      const [v, setV] = useState<string[]>([]);
      return <CompanyFilter companies={cos} value={v} onChange={(ids) => { calls.push(ids); setV(ids); }} />;
    }
    render(<Host />);
    await userEvent.click(screen.getByRole('button', { name: 'Filtrar por instância' }));
    expect(screen.getByLabelText('Todas as instâncias')).toBeChecked();
    await userEvent.click(screen.getByLabelText('Beta'));
    await userEvent.click(screen.getByLabelText('Gama'));
    expect(calls).toEqual([['B'], ['B', 'C']]);
    expect(screen.getByLabelText('Todas as instâncias')).not.toBeChecked();
    await userEvent.click(screen.getByLabelText('Todas as instâncias'));
    expect(calls[2]).toEqual([]);
    expect(screen.getByRole('button', { name: 'Filtrar por instância' })).toHaveTextContent('Todas as instâncias');
  });
});

describe('ConversationsEntry', () => {
  // the person's OWN instances (memberships); empty means a Hub-only person
  let mine: object[] = [{ id: 'T1', name: 'Minha instância' }];
  beforeEach(() => { mine = [{ id: 'T1', name: 'Minha instância' }]; });
  const tenants = (url: string) => (String(url).endsWith('/tenants') ? { data: mine } : null);
  const inbox = (companies: { id: string; name: string }[]) =>
    vi.mocked(axios.get).mockImplementation(async (url: string, cfg?: unknown) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
      if (tenants(url)) return tenants(url);
      if (String(url).includes('/inbox')) {
        const params = (cfg as { params?: Record<string, string> })?.params ?? {};
        return { data: { items: [], companies, has_more: false, count: 0, limit: Number(params.limit ?? 30) } };
      }
      return Promise.reject({ response: { status: 404 } });
    });

  it('keeps the classic workspace for someone with no Hub', async () => {
    hubs = [];
    inbox([]);
    renderAt(<ConversationsEntry />);
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
  });

  it('keeps the classic workspace for someone who serves one company through the Hub', async () => {
    inbox([{ id: 'A', name: 'Alfa' }]);
    renderAt(<ConversationsEntry />);
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
  });

  it('a person with NO instance of their own and ONE authorized instance still gets the Hub inbox (no empty classic workspace)', async () => {
    mine = [];
    inbox([{ id: 'A', name: 'Alfa' }]);
    renderAt(<ConversationsEntry />);
    expect(await screen.findByRole('heading', { name: 'Conversas' })).toBeInTheDocument();
    expect(screen.queryByText('Caixa completa da instância')).toBeNull();
  });

  it('shows every company in one inbox, with the company filter, for someone who serves two or more', async () => {
    inbox([{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }]);
    renderAt(<ConversationsEntry />);
    expect(await screen.findByRole('heading', { name: 'Conversas' })).toBeInTheDocument();
    expect(await screen.findByRole('button', { name: 'Filtrar por instância' })).toHaveTextContent('Todas as instâncias');
    expect(screen.queryByText('Caixa completa da instância')).toBeNull();
    // the default request carries NO company filter: everything the server authorizes
    const inboxCalls = vi.mocked(axios.get).mock.calls.filter((c) => String(c[0]).includes('/inbox'));
    expect(inboxCalls.every((c) => !('companies' in ((c[1] as { params: object }).params)))).toBe(true);
  });

  it('narrows the server request when companies are picked', async () => {
    inbox([{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }]);
    renderAt(<ConversationsEntry />);
    await userEvent.click(await screen.findByRole('button', { name: 'Filtrar por instância' }));
    await userEvent.click(screen.getByLabelText('Beta'));
    await waitFor(() => {
      const last = vi.mocked(axios.get).mock.calls.filter((c) => String(c[0]).includes('/inbox')).at(-1)!;
      expect((last[1] as { params: Record<string, string> }).params.companies).toBe('B');
    });
  });

  it('keeps the filter on screen while the narrower request is still loading', async () => {
    let release: () => void = () => undefined;
    const gate = new Promise<void>((r) => { release = r; });
    vi.mocked(axios.get).mockImplementation(async (url: string, cfg?: unknown) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
      if (tenants(url)) return tenants(url);
      const params = (cfg as { params?: Record<string, string> })?.params ?? {};
      if (params.companies) await gate; // the filtered request hangs
      return { data: { items: [], companies: [{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }], has_more: false, count: 0, limit: 30 } };
    });
    renderAt(<ConversationsEntry />);
    await userEvent.click(await screen.findByRole('button', { name: 'Filtrar por instância' }));
    await userEvent.click(screen.getByLabelText('Beta'));
    // still there, still open, and the choice is shown as made
    expect(screen.getByRole('button', { name: 'Filtrar por instância' })).toHaveTextContent('Beta');
    expect(screen.getByLabelText('Beta')).toBeChecked();
    release();
  });

  it('with two hubs, the one that serves two or more companies is used (not just the first)', async () => {
    hubs = [{ id: 'hub-1', name: 'Primeiro', role: 'hub_agent' }, { id: 'hub-2', name: 'Segundo', role: 'hub_agent' }];
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
      if (tenants(url)) return tenants(url);
      if (String(url).includes('/hubs/hub-2/inbox')) return { data: { items: [], companies: [{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }], has_more: false, count: 0, limit: 30 } };
      if (String(url).includes('/hubs/hub-1/inbox')) return { data: { items: [], companies: [{ id: 'Z', name: 'Zeta' }], has_more: false, count: 0, limit: 30 } };
      return Promise.reject({ response: { status: 404 } });
    });
    renderAt(<ConversationsEntry />);
    expect(await screen.findByRole('button', { name: 'Filtrar por instância' })).toBeInTheDocument();
    expect(screen.getByLabelText('Trocar de Hub')).toHaveValue('hub-2');
    expect(screen.queryByText('Caixa completa da instância')).toBeNull();
  });

  it('?modo=empresa asks for the classic workspace and never probes the Hub', async () => {
    inbox([{ id: 'A', name: 'Alfa' }, { id: 'B', name: 'Beta' }]);
    renderAt(<ConversationsEntry />, '/inbox?modo=empresa');
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).includes('/inbox'))).toBe(false);
  });

  it('falls back to the classic workspace when the Hub answers with an error', async () => {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
      return Promise.reject({ response: { status: 500 } });
    });
    renderAt(<ConversationsEntry />);
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
  });
});
