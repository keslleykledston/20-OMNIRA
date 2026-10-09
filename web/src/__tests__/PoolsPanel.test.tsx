import { beforeEach, describe, expect, it, vi } from 'vitest';
import { fireEvent, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import PoolsPanel from '../components/hub/PoolsPanel';
import { renderAt, setSession } from './testUtils';
import type { AccessAgent, AccessInstance, WorkPool } from '../lib/hub';

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const agent = (id: string, email: string, name = ''): AccessAgent => ({ user_id: id, email, name, hub_role: 'hub_agent', grants: [], direct_instances: [], instances: 0 });
const inst = (id: string, name: string): AccessInstance => ({ tenant_id: id, name, tenant_status: 'active', contract_status: 'active', admins: [], direct_agents: 0, hub_agents: 0 });
const agents = [agent('u1', 'ana@k3g.com', 'Ana'), agent('u2', 'beto@k3g.com')];
const instances = [inst('A', 'Alfa'), inst('B', 'Beta')];
let pools: WorkPool[] = [];

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (String(url).endsWith('/pools')) return { data: { items: pools } };
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  pools = [{
    id: 'p1', name: 'Suporte', description: '', distribution: 'manual',
    members: [{ user_id: 'u1', email: 'ana@k3g.com', name: 'Ana', max_open: 4, load: 2 }],
    instances: [{ tenant_id: 'A', name: 'Alfa' }, { tenant_id: 'B', name: 'Beta', queue_id: 'q-fila' }],
  }];
  serve();
});

describe('PoolsPanel', () => {
  it('shows each team with its members (capacity, load) and the instances it answers for', async () => {
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    const t = await screen.findByRole('article', { name: 'Equipe Suporte' });
    expect(within(t).getByLabelText('ana@k3g.com na equipe Suporte')).toBeChecked();
    expect(within(t).getByLabelText('beto@k3g.com na equipe Suporte')).not.toBeChecked();
    expect(within(t).getByLabelText('Capacidade de ana@k3g.com na equipe Suporte')).toHaveValue(4);
    expect(within(t).getByText('2 abertas')).toBeInTheDocument();
    expect(within(t).getByLabelText('Alfa atendida pela equipe Suporte')).toBeChecked();
    expect(within(t).getByLabelText('Beta atendida pela equipe Suporte')).not.toBeChecked(); // only a queue of Beta is configured
    expect(within(t).getByText(/1 atendimento\(s\) por fila/)).toBeInTheDocument();
  });

  it('creates a team (manual by default) and says what to do next', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: { id: 'p2', name: 'Novo', distribution: 'manual', description: '', members: [], instances: [] } });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    await userEvent.type(await screen.findByRole('textbox', { name: 'Nova equipe' }), '  Vendas ');
    await userEvent.click(screen.getByRole('button', { name: 'Criar equipe' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/pools$/);
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ name: 'Vendas', distribution: 'manual' });
    expect(await screen.findByRole('alert')).toHaveTextContent(/Equipe criada/);
  });

  it('switching to automatic distribution sends exactly that for that team', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: {} });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    await userEvent.selectOptions(await screen.findByLabelText('Distribuição da equipe Suporte'), 'round_robin');
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.patch).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/pools\/p1$/);
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ distribution: 'round_robin' });
  });

  it('saves the members with their capacity, and only the ones ticked', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: {} });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    const t = await screen.findByRole('article', { name: 'Equipe Suporte' });
    await userEvent.click(within(t).getByLabelText('beto@k3g.com na equipe Suporte'));
    const cap = within(t).getByLabelText('Capacidade de beto@k3g.com na equipe Suporte');
    fireEvent.change(cap, { target: { value: '7' } });
    await userEvent.click(within(t).getByRole('button', { name: 'Salvar integrantes' }));
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.put).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/pools\/p1\/members$/);
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ members: [{ user_id: 'u1', max_open: 4 }, { user_id: 'u2', max_open: 7 }] });
  });

  it('saving the instances keeps the queue-level entries exactly as they are', async () => {
    vi.mocked(axios.put).mockResolvedValue({ data: {} });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    const t = await screen.findByRole('article', { name: 'Equipe Suporte' });
    await userEvent.click(within(t).getByLabelText('Alfa atendida pela equipe Suporte')); // untick Alfa
    await userEvent.click(within(t).getByRole('button', { name: 'Salvar instâncias' }));
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1));
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ instances: [{ tenant_id: 'B', queue_id: 'q-fila' }] });
  });

  it('removing asks first and says what happens to conversations already assigned', async () => {
    vi.mocked(axios.delete).mockResolvedValue({ data: {} });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    await userEvent.click(await screen.findByRole('button', { name: 'Remover equipe' }));
    const dialog = await screen.findByRole('dialog');
    expect(within(dialog).getByText(/continuam com essa pessoa/)).toBeInTheDocument();
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancelar' }));
    expect(axios.delete).not.toHaveBeenCalled();
    await userEvent.click(screen.getByRole('button', { name: 'Remover equipe' }));
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Remover equipe' }));
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/pools\/p1$/);
  });

  it('a conflict is explained in words', async () => {
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 409, data: 'distribution: conflict: x' } });
    renderAt(<PoolsPanel hubId="hub-1" agents={agents} instances={instances} />);
    const t = await screen.findByRole('article', { name: 'Equipe Suporte' });
    await userEvent.click(within(t).getByRole('button', { name: 'Salvar instâncias' }));
    expect(await screen.findByRole('alert')).toHaveTextContent(/Outra equipe já atende/);
  });
});
