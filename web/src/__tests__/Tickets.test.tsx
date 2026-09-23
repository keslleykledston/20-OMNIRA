import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import axios from 'axios';
import TicketsPage from '../pages/Tickets';
import { renderAt, setSession, mockGets } from './testUtils';

// PRODUCT.2-B: real canonical ticket listing — no fixture/mock ticket data,
// no create/assign/resolve/close actions (not part of this slice's scope).
vi.mock('axios');

const ticket = (over: object = {}) => ({
  id: 't-' + Math.random().toString(36).slice(2),
  conversation_id: 'c-1',
  subject: 'Preciso de ajuda com meu pedido',
  status: 'open',
  priority: 'medium',
  assigned_to: null,
  created_at: '2026-09-20T10:00:00Z',
  updated_at: '2026-09-20T10:00:00Z',
  ...over,
});

beforeEach(() => {
  vi.clearAllMocks();
});

describe('TicketsPage', () => {
  it('renders real tickets from the API with status and priority', async () => {
    setSession();
    mockGets({
      '/tickets': { items: [ticket({ subject: 'Ticket real 1' })], has_more: false, count: 1, limit: 20 },
    });
    renderAt(<TicketsPage />);

    expect((await screen.findAllByText('Ticket real 1')).length).toBeGreaterThan(0);
    expect(screen.getAllByText('Média').length).toBeGreaterThan(0);
    expect(screen.getAllByText('Aberto').length).toBeGreaterThan(0);
  });

  it('shows a loading state before data arrives', () => {
    setSession();
    vi.mocked(axios.get).mockReturnValue(new Promise(() => {}) as any);
    renderAt(<TicketsPage />);
    expect(screen.getByRole('heading', { name: 'Tickets' })).toBeInTheDocument();
  });

  it('shows an empty state when there are no tickets', async () => {
    setSession();
    mockGets({ '/tickets': { items: [], has_more: false, count: 0, limit: 20 } });
    renderAt(<TicketsPage />);

    expect(await screen.findByText('Nenhum ticket encontrado')).toBeInTheDocument();
  });

  it('shows an error state when the request fails', async () => {
    setSession();
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 500 } });
    renderAt(<TicketsPage />);

    expect(await screen.findByText('Não foi possível carregar os tickets')).toBeInTheDocument();
  });

  it('shows a permission error message for a 403', async () => {
    setSession();
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 403 } });
    renderAt(<TicketsPage />);

    expect(await screen.findByText('Você não tem permissão para visualizar os tickets deste tenant.')).toBeInTheDocument();
  });

  it('sends the status filter to the API', async () => {
    setSession();
    mockGets({ '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 } });
    renderAt(<TicketsPage />);
    await screen.findAllByText('Preciso de ajuda com meu pedido');

    const { fireEvent } = await import('@testing-library/react');
    fireEvent.change(screen.getByLabelText('Filtrar por status'), { target: { value: 'closed' } });

    await screen.findAllByText('Preciso de ajuda com meu pedido');
    const call = [...vi.mocked(axios.get).mock.calls].reverse().find(([url]) => (url as string).endsWith('/tickets'));
    expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ status: 'closed' }) });
  });

  it('renders real data outside development (no UnavailableSurface)', async () => {
    vi.stubEnv('DEV', false);
    setSession();
    mockGets({ '/tickets': { items: [ticket({ subject: 'Real outside dev' })], has_more: false, count: 1, limit: 20 } });
    renderAt(<TicketsPage />);

    expect((await screen.findAllByText('Real outside dev')).length).toBeGreaterThan(0);
    expect(screen.queryByText('Este recurso ainda não está disponível nesta implantação.')).not.toBeInTheDocument();
    vi.unstubAllEnvs();
  });
});
