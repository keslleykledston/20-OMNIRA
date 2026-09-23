import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import axios from 'axios';
import SupervisorDashboardPage from '../pages/SupervisorDashboard';
import { renderAt, setSession, mockGets } from './testUtils';

// PRODUCT.1: real presence overview — no mock KPI/account/SLA content, real
// roster + real Valkey-backed presence snapshot only. usePresenceEvents opens
// an SSE connection via fetch(); reject it here so the effect's retry/backoff
// loop never resolves real events during the test (its own catch handles it,
// same as production when the stream drops).
vi.mock('axios');
global.fetch = vi.fn().mockRejectedValue(new Error('sse not available in test'));

const agent = (over: object = {}) => ({
  id: 'a-' + Math.random().toString(36).slice(2),
  membership_id: 'm-1',
  user_id: 'u-1',
  name: 'Ana Souza',
  email: 'ana@empresa.com',
  role: 'tenant_agent',
  status: 'active',
  queues: [],
  ...over,
});

beforeEach(() => {
  vi.clearAllMocks();
  global.fetch = vi.fn().mockRejectedValue(new Error('sse not available in test'));
});

describe('SupervisorDashboard', () => {
  it('shows total/online/offline counts and roster presence from real snapshot data', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['agent.read'] },
      '/agents/presence': { online_agent_profile_ids: ['a-online'] },
      '/agents': { items: [agent({ id: 'a-online', name: 'Bruno Lima' }), agent({ id: 'a-offline', name: 'Clara Dias' })] },
    });
    renderAt(<SupervisorDashboardPage />);

    expect(await screen.findByRole('heading', { name: 'Supervisor' })).toBeInTheDocument();
    const table = await screen.findByRole('table');
    await within(table).findByText('Bruno Lima');

    const rows = within(table).getAllByRole('row');
    const brunoRow = rows.find((r) => r.textContent?.includes('Bruno Lima'));
    const claraRow = rows.find((r) => r.textContent?.includes('Clara Dias'));
    expect(brunoRow).toHaveTextContent('Online');
    expect(claraRow).toHaveTextContent('Offline');

    expect(screen.getByText('Total de agentes').nextElementSibling).toHaveTextContent('2');
    expect(screen.getAllByText('Online')[0].nextElementSibling).toHaveTextContent('1');
    expect(screen.getAllByText('Offline')[0].nextElementSibling).toHaveTextContent('1');
  });

  it('does not render mock account/SLA/KPI content', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['agent.read'] },
      '/agents/presence': { online_agent_profile_ids: [] },
      '/agents': { items: [] },
    });
    renderAt(<SupervisorDashboardPage />);

    await screen.findByRole('heading', { name: 'Supervisor' });
    expect(screen.queryByText(/Conformidade SLA/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Saúde das Contas/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Atividades Recentes/)).not.toBeInTheDocument();
    expect(screen.queryByText(/Test Company LTDA/)).not.toBeInTheDocument();
  });

  it('shows a permission error for a user without agent.read', async () => {
    setSession();
    mockGets({ '/me/access': { permissions: [] } });
    renderAt(<SupervisorDashboardPage />);

    expect(await screen.findByText('Você não tem permissão para visualizar o supervisor.')).toBeInTheDocument();
  });

  it('renders real data outside development (no UnavailableSurface)', async () => {
    vi.stubEnv('DEV', false);
    setSession();
    mockGets({
      '/me/access': { permissions: ['agent.read'] },
      '/agents/presence': { online_agent_profile_ids: [] },
      '/agents': { items: [agent({ name: 'Diego Reis' })] },
    });
    renderAt(<SupervisorDashboardPage />);

    expect((await screen.findAllByText('Diego Reis')).length).toBeGreaterThan(0);
    expect(screen.queryByText('Este recurso ainda não está disponível nesta implantação.')).not.toBeInTheDocument();
    vi.unstubAllEnvs();
  });
});
