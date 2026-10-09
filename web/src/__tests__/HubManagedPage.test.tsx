import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import axios from 'axios';
import HubManagedPage, { HubManagedChannelsRoute } from '../pages/HubManagedPage';
import { renderAt, setSession } from './testUtils';

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

let hubs: object[] = [];
let managed: object[] = [];

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.endsWith('/hubs')) return { data: { items: hubs } };
    if (u.endsWith('/managed')) return { data: { items: managed } };
    if (u.endsWith('/channels/providers')) return { data: { items: [] } };
    if (u.endsWith('/channels/connections')) return { data: { items: [] } };
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_agent', can_manage_instances: true }];
  managed = [{ tenant_id: 'A', name: 'ISP Roraima', scopes: ['channels', 'integrations'] }, { tenant_id: 'B', name: 'NorteNet', scopes: ['integrations'] }];
  serve();
});

describe('HubManagedPage', () => {
  it('lists only what the server says this person may manage, with the scopes, and links to each instance', async () => {
    renderAt(<HubManagedPage />);
    const a = (await screen.findByText('ISP Roraima')).closest('li')!;
    expect(within(a).getByText(/Canais de atendimento · Integrações de retaguarda/)).toBeInTheDocument();
    expect(within(a).getByRole('link', { name: 'Abrir canais e integrações' })).toHaveAttribute('href', '/instancias/hub-1/A/canais');
    const b = screen.getByText('NorteNet').closest('li')!;
    expect(within(b).getByText('Integrações de retaguarda')).toBeInTheDocument();
    expect(within(b).queryByText(/Canais \(WhatsApp\)/)).toBeNull();
  });

  it('says so, and asks for nothing, when nothing is delegated', async () => {
    hubs = [{ id: 'hub-1', name: 'K3G Solutions', role: 'hub_agent', can_manage_instances: false }];
    renderAt(<HubManagedPage />);
    expect(await screen.findByText('Nenhuma instância para gerenciar')).toBeInTheDocument();
    expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).endsWith('/managed'))).toBe(false);
  });
});

describe('HubManagedChannelsRoute', () => {
  it('the SAME channels screen talks to the hub routes of THAT instance, never to the tenant routes', async () => {
    renderAt(<HubManagedChannelsRoute />, '/instancias/hub-1/A/canais', '/instancias/:hubId/:tenantId/canais');
    await waitFor(() => expect(vi.mocked(axios.get).mock.calls.some((c) => String(c[0]).endsWith('/hubs/hub-1/instances/A/channels/connections'))).toBe(true));
    const urls = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]));
    expect(urls.some((u) => u.includes('/tenants/'))).toBe(false);
    expect(await screen.findByText(/Canais e integrações — ISP Roraima/)).toBeInTheDocument();
  });
});
