import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import CompaniesPanel from '../components/hub/CompaniesPanel';
import { renderAt, setSession } from './testUtils';
import type { HubCompany } from '../lib/hub';

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const CAPS = [
  { key: 'whatsapp_channel', label: 'Canal WhatsApp', description: 'Conectar linhas.', gates: 'Criar nova conexão de WhatsApp.' },
  { key: 'erp_crm', label: 'ERP / CRM', description: 'Integrar retaguarda.', gates: 'Criar nova integração.' },
  { key: 'outbound_attachments', label: 'Anexos de saída', description: 'Enviar arquivos.', gates: 'Enviar anexos.' },
];
const company = (id: string, over: Partial<HubCompany> = {}): HubCompany => ({
  id, legal_name: `Instância ${id} Ltda`, trade_name: `Instância ${id}`, display_name: `Instância ${id}`, status: 'active', contract_status: 'active',
  capabilities: { whatsapp_channel: true, erp_crm: true, outbound_attachments: true }, channels: 1, integrations: 0, open_conversations: 3, agents: 2,
  created_at: new Date().toISOString(), ...over,
});

let hubs: object[] = [];
let companies: HubCompany[] = [];

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (String(url).endsWith('/hubs')) return { data: { items: hubs } };
    if (String(url).endsWith('/companies')) return { data: { items: companies, capabilities: CAPS } };
    if (String(url).includes('/inbox')) return { data: { items: [], has_more: false, count: 0, limit: 30 } };
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_admin', can_manage_companies: true }];
  companies = [company('A'), company('B', { status: 'suspended' })];
  serve();
});

describe('CompaniesPanel', () => {
  it('lists the companies with status, counters and capability switches', async () => {
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    expect(within(a).getByText('Ativa')).toBeInTheDocument();
    expect(within(a).getByText('Conversas abertas').nextSibling).toHaveTextContent('3');
    expect(within(a).getByLabelText(/Canal WhatsApp/)).toBeChecked();
    const b = screen.getByRole('region', { name: 'Instância Instância B' });
    expect(within(b).getByText('Suspensa')).toBeInTheDocument();
    expect(within(b).getByRole('button', { name: 'Reativar' })).toBeInTheDocument();
  });

  it('switching a capability sends exactly that one change for exactly that company', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: company('A', { capabilities: { whatsapp_channel: false, erp_crm: true, outbound_attachments: true } }) });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(within(a).getByLabelText(/Canal WhatsApp/));
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1));
    const [url, body] = vi.mocked(axios.patch).mock.calls[0];
    expect(String(url)).toMatch(/\/hubs\/hub-1\/companies\/A$/);
    expect(body).toEqual({ capabilities: { whatsapp_channel: false } });
  });

  it('delegating management sends the whole new list of scopes for exactly that company, and withdrawing sends what is left', async () => {
    companies = [company('A', { management_scopes: ['channels'] }), company('B')];
    serve();
    vi.mocked(axios.patch).mockResolvedValue({ data: company('A') });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    expect(within(a).getByLabelText(/Canais de atendimento/)).toBeChecked();
    expect(within(a).getByLabelText(/Integrações de retaguarda/)).not.toBeChecked();
    expect(within(a).getByRole('link', { name: 'Abrir canais e integrações' })).toHaveAttribute('href', '/instancias/hub-1/A/canais');
    await userEvent.click(within(a).getByLabelText(/Integrações de retaguarda/));
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1));
    expect(String(vi.mocked(axios.patch).mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/companies\/A$/);
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ management_scopes: ['channels', 'integrations'] });
    await userEvent.click(within(a).getByLabelText(/Canais de atendimento/));
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(2));
    expect(vi.mocked(axios.patch).mock.calls[1][1]).toEqual({ management_scopes: [] });
    // a company that delegates nothing offers no way into the channels screen
    const b = screen.getByRole('region', { name: 'Instância Instância B' });
    expect(within(b).queryByRole('link', { name: 'Abrir canais e integrações' })).toBeNull();
  });

  it('suspending asks first and says what happens; cancelling changes nothing', async () => {
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(within(a).getByRole('button', { name: 'Suspender' }));
    expect(await screen.findByText(/perdem o acesso na hora/)).toBeInTheDocument();
    await userEvent.click(screen.getByRole('button', { name: 'Cancelar' }));
    expect(axios.patch).not.toHaveBeenCalled();
  });

  it('confirming suspends; reactivating needs no confirmation', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: company('A', { status: 'suspended' }) });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(within(a).getByRole('button', { name: 'Suspender' }));
    await userEvent.click(await screen.findByRole('button', { name: 'Suspender instância' }));
    await waitFor(() => expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ status: 'suspended' }));
    const b = screen.getByRole('region', { name: 'Instância Instância B' });
    await userEvent.click(within(b).getByRole('button', { name: 'Reativar' }));
    await waitFor(() => expect(vi.mocked(axios.patch).mock.calls[1][1]).toEqual({ status: 'active' }));
  });

  it('a refused change is explained, not silently ignored', async () => {
    vi.mocked(axios.patch).mockRejectedValue({ response: { status: 404 } });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    const a = await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(within(a).getByLabelText(/ERP/));
    expect(await screen.findByRole('alert')).toHaveTextContent('não tem permissão');
  });

  it('creating sends the typed fields, one Idempotency-Key per attempt, and says nobody has access yet', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: company('N', { display_name: 'Nova', legal_name: 'Nova Ltda' }) });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(screen.getByRole('button', { name: 'Nova instância' }));
    const dialog = await screen.findByRole('dialog', { name: 'Nova instância' });
    await userEvent.type(within(dialog).getByLabelText(/Razão social/), 'Nova Ltda');
    await userEvent.type(within(dialog).getByLabelText(/Nome fantasia/), 'Nova');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Criar instância' }));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    const [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, Record<string, unknown>, { headers: Record<string, string> }];
    expect(url).toMatch(/\/hubs\/hub-1\/companies$/);
    expect(body).toEqual({ legal_name: 'Nova Ltda', trade_name: 'Nova', tax_id: undefined, initial_admin_email: undefined });
    expect(cfg.headers['Idempotency-Key']).toMatch(/^new-company-/);
    expect(await screen.findByText(/Ninguém tem acesso a ela ainda/)).toBeInTheDocument();
  });

  it('a retry of the same content reuses the key; an edit starts a new attempt', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422 }, });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(screen.getByRole('button', { name: 'Nova instância' }));
    const dialog = await screen.findByRole('dialog', { name: 'Nova instância' });
    await userEvent.type(within(dialog).getByLabelText(/Razão social/), 'Repetida Ltda');
    const create = within(dialog).getByRole('button', { name: 'Criar instância' });
    await userEvent.click(create);
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1));
    await userEvent.click(create);
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(2));
    const key = (i: number) => (vi.mocked(axios.post).mock.calls[i][2] as { headers: Record<string, string> }).headers['Idempotency-Key'];
    expect(key(1)).toBe(key(0));
    await userEvent.type(within(dialog).getByLabelText(/Razão social/), ' S.A.');
    await userEvent.click(create);
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(3));
    expect(key(2)).not.toBe(key(0));
  });

  it('shows the server message when the administrator e-mail is not an existing user', async () => {
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422, data: 'companies: invalid request: initial_admin_email must match exactly one existing user' } });
    renderAt(<CompaniesPanel hubId="hub-1" />);
    await screen.findByRole('region', { name: 'Instância Instância A' });
    await userEvent.click(screen.getByRole('button', { name: 'Nova instância' }));
    const dialog = await screen.findByRole('dialog', { name: 'Nova instância' });
    await userEvent.type(within(dialog).getByLabelText(/Razão social/), 'Com Admin');
    await userEvent.type(within(dialog).getByLabelText(/primeiro administrador/), 'ninguem@example.test');
    await userEvent.click(within(dialog).getByRole('button', { name: 'Criar instância' }));
    expect(await within(dialog).findByRole('alert')).toHaveTextContent('exactly one existing user');
  });
});
