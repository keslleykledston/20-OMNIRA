import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor, within } from '@testing-library/react';
import axios from 'axios';
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
  it('shows the unclassified-contacts backlog next to the real metric cards (ADR-0018)', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['dashboard.read', 'agent.read'] },
      '/dashboard/snapshot': { open_conversations: 3, open_tickets: 5, total_contacts: 12, unclassified_contacts: 7 },
      '/agents/presence': { online_agent_profile_ids: [] },
    });
    renderAt(<Dashboard />);
    const backlog = await screen.findByText('Não classificados');
    expect(within(backlog.parentElement!).getByText('7')).toBeInTheDocument();
  });

  it('renders the real V1 metric cards from the real snapshot and presence sources', async () => {
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

  // UX audit M5
  it('every metric card is a link to the list its number counts', async () => {
    setSession();
    mockGets({
      '/me/access': { permissions: ['dashboard.read', 'agent.read'] },
      '/dashboard/snapshot': { open_conversations: 3, open_tickets: 5, total_contacts: 12, unclassified_contacts: 7 },
      '/agents/presence': { online_agent_profile_ids: [] },
    });
    renderAt(<Dashboard />);
    await screen.findByText('Conversas abertas');
    expect(screen.getByRole('link', { name: /Conversas abertas: 3/ })).toHaveAttribute('href', '/inbox');
    expect(screen.getByRole('link', { name: /Tickets abertos: 5/ })).toHaveAttribute('href', '/tickets');
    expect(screen.getByRole('link', { name: /^Contatos: 12/ })).toHaveAttribute('href', '/contacts');
    expect(screen.getByRole('link', { name: /Não classificados: 7/ })).toHaveAttribute('href', '/contacts');
    expect(screen.getByRole('link', { name: /Agentes online/ })).toHaveAttribute('href', '/supervisor');
  });

  describe('Filas que pedem atenção', () => {
    const queue = (id: string, name: string, open: number) => ({ id, name, mode: 'manual', is_default: false, member_count: 2, available_count: 1, open_conversation_count: open, created_at: '', updated_at: '' });
    const clocks = (counts: Record<string, number>) => ({ queue_id: 'x', counts: { running: 0, at_risk: 0, breached: 0, ...counts }, items: [] });
    const base = { '/me/access': { permissions: ['dashboard.read', 'agent.read'] }, '/dashboard/snapshot': { open_conversations: 1, open_tickets: 1, total_contacts: 1 }, '/agents/presence': { online_agent_profile_ids: [] } };
    const failQueues = (code: number) =>
      vi.mocked(axios.get).mockImplementation(async (url: string) => {
        if (url.endsWith('/me/access')) return { data: base['/me/access'] };
        if (url.endsWith('/dashboard/snapshot')) return { data: base['/dashboard/snapshot'] };
        if (url.endsWith('/agents/presence')) return { data: base['/agents/presence'] };
        return Promise.reject({ response: { status: code } });
      });

    it('lists only the queues with a clock at risk or breached, the breached first, from the real SLA clocks', async () => {
      setSession();
      mockGets({
        ...base,
        '/queues/q-fin/sla': clocks({ at_risk: 2 }),
        '/queues/q-sup/sla': clocks({ breached: 1, at_risk: 1 }),
        '/queues/q-com/sla': clocks({ running: 3 }),
        '/queues/q-vazia/sla': clocks({ at_risk: 1 }), // a queue with no open conversation is never asked about
        '/queues': { items: [queue('q-fin', 'Financeiro', 6), queue('q-sup', 'Suporte', 14), queue('q-com', 'Comercial', 4), queue('q-vazia', 'Sem conversas', 0)] },
      });
      renderAt(<Dashboard />);
      const section = await screen.findByRole('region', { name: 'Filas que pedem atenção' });
      const items = await within(section).findAllByRole('listitem');
      expect(items.map((li) => li.querySelector('p')?.textContent)).toEqual(['Suporte', 'Financeiro']); // Comercial is on time, the empty one has no clock
      expect(items[0]).toHaveTextContent('SLA estourado');
      expect(items[0]).toHaveTextContent('14 conversas abertas · 1 estourada · 1 em risco');
      expect(items[1]).toHaveTextContent('SLA em risco');
      expect(within(section).getByRole('link', { name: 'Ver monitor de SLA' })).toHaveAttribute('href', '/settings/attendance/overflow');
    });

    it('says all is well when no queue has a clock at risk, instead of an empty box', async () => {
      setSession();
      mockGets({ ...base, '/queues/q-com/sla': clocks({ running: 3 }), '/queues': { items: [queue('q-com', 'Comercial', 4)] } });
      renderAt(<Dashboard />);
      expect(await screen.findByText('Tudo em dia: nenhuma fila com SLA em risco agora.')).toBeInTheDocument();
    });

    it('does not exist for a person who may not read the queues (403)', async () => {
      setSession();
      failQueues(403);
      renderAt(<Dashboard />);
      await screen.findByText('Conversas abertas');
      await waitFor(() => expect(screen.queryByRole('region', { name: 'Filas que pedem atenção' })).not.toBeInTheDocument());
    });

    it('shows an error with retry for any other failure', async () => {
      setSession();
      failQueues(500);
      renderAt(<Dashboard />);
      expect(await screen.findByText('Não foi possível carregar as filas.')).toBeInTheDocument();
      expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument();
    });

    it('is not drawn for someone without agent.read', async () => {
      setSession();
      // the queues would answer: it is the permission, not the server's refusal, that keeps the block off the screen
      mockGets({ ...base, '/me/access': { permissions: ['dashboard.read'] }, '/queues/q-sup/sla': clocks({ breached: 1 }), '/queues': { items: [queue('q-sup', 'Suporte', 3)] } });
      renderAt(<Dashboard />);
      await screen.findByText('Conversas abertas');
      expect(screen.queryByRole('region', { name: 'Filas que pedem atenção' })).not.toBeInTheDocument();
    });

    it('says so when the clock of some queue could not be read, instead of claiming all is well', async () => {
      setSession();
      vi.mocked(axios.get).mockImplementation(async (url: string) => {
        if (url.endsWith('/me/access')) return { data: base['/me/access'] };
        if (url.endsWith('/dashboard/snapshot')) return { data: base['/dashboard/snapshot'] };
        if (url.endsWith('/agents/presence')) return { data: base['/agents/presence'] };
        if (url.endsWith('/queues')) return { data: { items: [queue('q-sup', 'Suporte', 3)] } };
        return Promise.reject({ response: { status: 500 } });
      });
      renderAt(<Dashboard />);
      expect(await screen.findByText('Não foi possível ler o relógio de SLA de algumas filas.')).toBeInTheDocument();
      expect(screen.queryByText(/Tudo em dia/)).not.toBeInTheDocument();
    });
  });
});
