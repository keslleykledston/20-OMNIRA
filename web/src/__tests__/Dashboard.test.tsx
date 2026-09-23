import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, within } from '@testing-library/react';
import Dashboard from '../pages/Dashboard';
import { renderAt, setSession, mockGets } from './testUtils';

// PRODUCT.3-B: real V1 operational snapshot only — no chart, no trend, no
// fabricated activity feed. usePresenceEvents opens an SSE connection via
// fetch(); reject it here so its retry/backoff loop never resolves real
// events during the test (its own catch handles it, same as production).
vi.mock('axios');
global.fetch = vi.fn().mockRejectedValue(new Error('sse not available in test'));

beforeEach(() => {
  vi.clearAllMocks();
  global.fetch = vi.fn().mockRejectedValue(new Error('sse not available in test'));
});

describe('Dashboard', () => {
  it('renders the four real V1 metric cards from the real snapshot and presence sources', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['dashboard.read', 'agent.read'] },
      '/dashboard/snapshot': { open_conversations: 3, open_tickets: 5, total_contacts: 12 },
      '/agents/presence': { online_agent_profile_ids: ['a-1', 'a-2'] },
    });
    renderAt(<Dashboard />);

    const openConversations = await screen.findByText('Conversas abertas');
    expect(within(openConversations.parentElement!).getByText('3')).toBeInTheDocument();

    const openTickets = screen.getByText('Tickets abertos');
    expect(within(openTickets.parentElement!).getByText('5')).toBeInTheDocument();

    const totalContacts = screen.getByText('Contatos');
    expect(within(totalContacts.parentElement!).getByText('12')).toBeInTheDocument();

    const agentsOnline = await screen.findByText('Agentes online');
    expect(within(agentsOnline.parentElement!).getByText('2')).toBeInTheDocument();
  });

  it('does not render chart/trend/fake-activity content from the retired mock dashboard', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['dashboard.read', 'agent.read'] },
      '/dashboard/snapshot': { open_conversations: 0, open_tickets: 0, total_contacts: 0 },
      '/agents/presence': { online_agent_profile_ids: [] },
    });
    renderAt(<Dashboard />);

    await screen.findByText('Conversas abertas');
    expect(screen.queryByText('Conversas por canal')).not.toBeInTheDocument();
    expect(screen.queryByText('Status dos tickets')).not.toBeInTheDocument();
    expect(screen.queryByText('Conversas recentes')).not.toBeInTheDocument();
    expect(screen.queryByText('Tickets prioritários')).not.toBeInTheDocument();
    expect(screen.queryByText(/Conformidade SLA/)).not.toBeInTheDocument();
  });

  it('shows an error state when the snapshot request fails', async () => {
    setSession();
    mockGets({ '/me/access': { permissions: ['dashboard.read'] } });
    const axios = (await import('axios')).default;
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { permissions: ['dashboard.read'] } };
      return Promise.reject({ response: { status: 500 } });
    });
    renderAt(<Dashboard />);

    expect(await screen.findByText('Não foi possível carregar os indicadores')).toBeInTheDocument();
  });

  it('shows a permission error for a user without dashboard.read', async () => {
    setSession();
    mockGets({ '/me/access': { permissions: [] } });
    renderAt(<Dashboard />);

    expect(await screen.findByText('Você não tem permissão para visualizar o Dashboard.')).toBeInTheDocument();
  });

  it('renders real data outside development (no UnavailableSurface)', async () => {
    vi.stubEnv('DEV', false);
    setSession();
    mockGets({
      '/me/access': { permissions: ['dashboard.read', 'agent.read'] },
      '/dashboard/snapshot': { open_conversations: 1, open_tickets: 1, total_contacts: 1 },
      '/agents/presence': { online_agent_profile_ids: [] },
    });
    renderAt(<Dashboard />);

    await screen.findByText('Conversas abertas');
    expect(screen.queryByText('Este recurso ainda não está disponível nesta implantação.')).not.toBeInTheDocument();
    vi.unstubAllEnvs();
  });
});
