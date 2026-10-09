import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import AuditPanel from '../components/hub/AuditPanel';
import { renderAt, setSession } from './testUtils';
import type { AccessInstance, AuditEvent } from '../lib/hub';

vi.mock('axios');
const unauthorized = vi.hoisted(() => vi.fn());
vi.mock('../lib/session', async (orig) => ({ ...(await orig<typeof import('../lib/session')>()), handleUnauthorized: unauthorized }));

const inst = (id: string, name: string): AccessInstance => ({ tenant_id: id, name, tenant_status: 'active', contract_status: 'active', admins: [], direct_agents: 0, hub_agents: 0 });
const instances = [inst('A', 'Alfa'), inst('B', 'Beta')];
const ev = (n: number, over: Partial<AuditEvent> = {}): AuditEvent => ({ id: `e${n}`, at: new Date(2026, 9, 9, 10, 60 - n).toISOString(), action: 'hub.grant.granted', tenant_id: 'A', tenant_name: 'Alfa', actor_email: 'adm@k3g.com', actor_name: 'Ana Admin', ...over });
let pages: Record<string, { items: AuditEvent[]; next?: string }> = {};

beforeEach(() => {
  vi.resetAllMocks();
  unauthorized.mockReset();
  localStorage.clear();
  setSession();
  pages = {
    '': { items: [ev(1), ev(2, { action: 'channel.connection_created', via: 'hub', facts: { provider: 'k3g_crm', host: 'crm.exemplo.test' }, actor_name: '' }), ev(3, { action: 'estranha.acao', actor_email: undefined, actor_name: '', tenant_name: '' })], next: 'cursor-1' },
    'cursor-1': { items: [ev(4, { action: 'platform.company.management_scopes_changed', facts: { from: ['channels'], to: ['channels', 'integrations'] } })] },
  };
  vi.mocked(axios.get).mockImplementation(async (_url: string, cfg?: unknown) => {
    const params = (cfg as { params?: { before?: string; tenant?: string } })?.params ?? {};
    if (params.tenant === 'B') return { data: { items: [ev(9, { tenant_id: 'B', tenant_name: 'Beta' })] } };
    return { data: pages[params.before ?? ''] };
  });
});

describe('AuditPanel', () => {
  it('says what happened in words, who did it, through the hub when so, and keeps an unknown action visible', async () => {
    renderAt(<AuditPanel hubId="hub-1" instances={instances} />);
    const table = await screen.findByRole('table');
    expect(within(table).getByText('Acesso concedido')).toBeInTheDocument();
    expect(within(table).getByText('Conexão de canal/integração criada')).toBeInTheDocument();
    expect(within(table).getByText('pelo Hub')).toBeInTheDocument();
    expect(within(table).getByText(/provedor: k3g_crm · servidor: crm.exemplo.test/)).toBeInTheDocument();
    expect(within(table).getAllByText('Ana Admin').length).toBeGreaterThan(0);
    expect(within(table).getByText('estranha.acao')).toBeInTheDocument(); // never hidden
    expect(within(table).getByText('Sistema')).toBeInTheDocument(); // no actor recorded
  });

  it('loads older events on demand and narrows to one instance with the server doing the filtering', async () => {
    renderAt(<AuditPanel hubId="hub-1" instances={instances} />);
    await screen.findByRole('table');
    await userEvent.click(screen.getByRole('button', { name: 'Carregar mais' }));
    expect(await screen.findByText('Gestão delegada ao Hub alterada')).toBeInTheDocument();
    expect(screen.getByText(/de: channels · para: channels, integrations/)).toBeInTheDocument();
    await userEvent.selectOptions(screen.getByLabelText('Filtrar auditoria por instância'), 'B');
    await waitFor(() => expect(vi.mocked(axios.get).mock.calls.some((c) => (c[1] as { params: { tenant?: string } }).params.tenant === 'B')).toBe(true));
    expect(await within(await screen.findByRole('table')).findByText('Beta')).toBeInTheDocument();
  });

  it('an empty answer is said, not shown as an empty table', async () => {
    pages = { '': { items: [] } };
    renderAt(<AuditPanel hubId="hub-1" instances={instances} />);
    expect(await screen.findByText('Nada registrado')).toBeInTheDocument();
    expect(screen.queryByRole('table')).toBeNull();
  });
});
