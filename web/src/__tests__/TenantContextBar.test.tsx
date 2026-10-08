import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import ChatPane from '../components/inbox/ChatPane';
import TenantContextBar from '../components/inbox/TenantContextBar';
import { tenantCode } from '../lib/tenants';
import { renderAt, setSession, TENANT } from './testUtils';

vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({ useRealtimeEvents: () => {} }));

const CONV = 'conv-1';
type Tenants = { id: string; legal_name: string; trade_name?: string }[];

function serve(tenants: Tenants | 'fail') {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if ((url as string).endsWith('/tenants')) {
      if (tenants === 'fail') return Promise.reject({ response: { status: 500 } });
      return { data: tenants };
    }
    if ((url as string).endsWith(`/inbox/conversations/${CONV}`)) {
      return { data: { id: CONV, contact_name: 'Maria', contact_phone: '+5511999990000', status: 'active', assigned_to_user_id: 'u-1' } };
    }
    if ((url as string).endsWith('/messages')) return { data: { items: [], has_more: false } };
    return Promise.reject({ response: { status: 404 } });
  });
}

// "Hidden" must mean hidden, not "the tree crashed": React reports an uncaught render error through console.error.
async function expectHiddenWithoutErrors() {
  const errors = vi.spyOn(console, 'error').mockImplementation(() => {});
  await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled());
  await new Promise((r) => setTimeout(r, 40));
  expect(screen.queryByTestId('tenant-context-bar')).toBeNull();
  const real = errors.mock.calls.map((c) => String(c[0]).slice(0, 200)).filter((m) => !m.includes('not wrapped in act')); // act() noise is not a render error
  expect(real).toEqual([]);
  errors.mockRestore();
}

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  setSession();
});

describe('tenantCode', () => {
  it.each([
    ['ISP Roraima', 'IR'],
    ['NorteNet', 'NOR'],
    ['K3G Solutions', 'KS'],
    ['Telecomunicações do Norte Ltda', 'TN'],
    ['  empresa   de   água e luz ', 'EAL'],
    ['Açaí', 'ACA'],
    ['Alfa Beta Gama Delta', 'ABG'],
    ['', '?'],
    ['---', '?'],
    ['de da do', '?'],
  ])('%j -> %s', (name, code) => {
    expect(tenantCode(name)).toBe(code);
  });
});

describe('TenantContextBar', () => {
  it('names the company for someone who serves several, with the full name (not only a code)', async () => {
    serve([
      { id: TENANT, legal_name: 'ISP Roraima Ltda', trade_name: 'ISP Roraima' },
      { id: 'tenant-b', legal_name: 'NorteNet' },
    ]);
    renderAt(<TenantContextBar />);
    const bar = await screen.findByTestId('tenant-context-bar');
    expect(bar).toHaveTextContent('Atendendo');
    expect(bar).toHaveTextContent('ISP Roraima'); // trade name wins over legal name
    expect(bar).not.toHaveTextContent('Ltda');
    expect(bar).toHaveAttribute('aria-label', 'Atendendo a empresa ISP Roraima');
    expect(bar).toHaveTextContent('IR');
  });

  it('shows the company the SESSION points at, not another one of the list', async () => {
    localStorage.setItem('tenantId', 'tenant-b');
    serve([
      { id: TENANT, legal_name: 'ISP Roraima' },
      { id: 'tenant-b', legal_name: 'NorteNet' },
    ]);
    renderAt(<TenantContextBar />);
    const bar = await screen.findByTestId('tenant-context-bar');
    expect(bar).toHaveTextContent('NorteNet');
    expect(bar).not.toHaveTextContent('ISP Roraima');
  });

  it('adds nothing for someone who belongs to a single company', async () => {
    serve([{ id: TENANT, legal_name: 'ISP Roraima' }]);
    renderAt(<TenantContextBar />);
    await expectHiddenWithoutErrors();
  });

  it('shows nothing when the session company is not in the server list (never invents one)', async () => {
    localStorage.setItem('tenantId', 'someone-elses-tenant');
    serve([
      { id: TENANT, legal_name: 'ISP Roraima' },
      { id: 'tenant-b', legal_name: 'NorteNet' },
    ]);
    renderAt(<TenantContextBar />);
    await expectHiddenWithoutErrors();
  });

  it('degrades silently when the list cannot be loaded', async () => {
    serve('fail');
    renderAt(<TenantContextBar />);
    await expectHiddenWithoutErrors();
  });
});

describe('ChatPane with the tenant context bar', () => {
  it('shows the bar for a multi-company operator and keeps the conversation working', async () => {
    serve([
      { id: TENANT, legal_name: 'ISP Roraima' },
      { id: 'tenant-b', legal_name: 'NorteNet' },
    ]);
    renderAt(<ChatPane conversationId={CONV} />);
    expect(await screen.findByTestId('tenant-context-bar')).toHaveTextContent('ISP Roraima');
    expect(await screen.findByRole('heading', { name: 'Maria' })).toBeInTheDocument();
    expect(screen.getByText('Em atendimento')).toBeInTheDocument();
  });

  it('is unchanged for a single-company user', async () => {
    serve([{ id: TENANT, legal_name: 'ISP Roraima' }]);
    renderAt(<ChatPane conversationId={CONV} />);
    expect(await screen.findByRole('heading', { name: 'Maria' })).toBeInTheDocument();
    await new Promise((r) => setTimeout(r, 30));
    expect(screen.queryByTestId('tenant-context-bar')).toBeNull();
  });
});
