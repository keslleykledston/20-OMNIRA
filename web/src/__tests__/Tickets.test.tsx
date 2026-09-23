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

  describe('CSV export', () => {
    it('shows the Exportar CSV action for a user with ticket.read', async () => {
      setSession();
      mockGets({
        '/me/access': { permissions: ['ticket.read'] },
        '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 },
      });
      renderAt(<TicketsPage />);

      expect(await screen.findByRole('button', { name: /Exportar CSV/ })).toBeInTheDocument();
    });

    it('does not show Exportar CSV for a user without ticket.read', async () => {
      setSession();
      mockGets({
        '/me/access': { permissions: [] },
        '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 },
      });
      renderAt(<TicketsPage />);

      await screen.findAllByText('Preciso de ajuda com meu pedido');
      expect(screen.queryByRole('button', { name: /Exportar CSV/ })).not.toBeInTheDocument();
    });

    it('downloads the export respecting the current status filter, not the visible page', async () => {
      setSession();
      vi.stubGlobal('URL', { ...URL, createObjectURL: vi.fn(() => 'blob:mock'), revokeObjectURL: vi.fn() });
      const clickSpy = vi.spyOn(HTMLAnchorElement.prototype, 'click').mockImplementation(() => {});

      vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
        if (url.endsWith('/me/access')) return { data: { permissions: ['ticket.read'] } };
        if (url.endsWith('/tickets/export.csv')) {
          return {
            data: new Blob(['id,subject\n1,Test'], { type: 'text/csv' }),
            headers: { 'content-disposition': 'attachment; filename="tickets.csv"' },
          };
        }
        if (url.endsWith('/tickets')) return { data: { items: [ticket()], has_more: false, count: 1, limit: 20 } };
        return Promise.reject({ response: { status: 404 } });
      });

      renderAt(<TicketsPage />);
      await screen.findAllByText('Preciso de ajuda com meu pedido');

      const { fireEvent } = await import('@testing-library/react');
      fireEvent.change(screen.getByLabelText('Filtrar por status'), { target: { value: 'closed' } });
      await screen.findByRole('button', { name: /Exportar CSV/ });

      fireEvent.click(screen.getByRole('button', { name: /Exportar CSV/ }));

      await vi.waitFor(() => expect(clickSpy).toHaveBeenCalled());
      const exportCall = vi.mocked(axios.get).mock.calls.find(([url]) => (url as string).endsWith('/tickets/export.csv'));
      expect(exportCall?.[1]).toMatchObject({ params: { status: 'closed' } });
      expect(URL.createObjectURL).toHaveBeenCalled();
      expect(URL.revokeObjectURL).toHaveBeenCalledWith('blob:mock');

      clickSpy.mockRestore();
      vi.unstubAllGlobals();
    });

    it('shows the Problem Details message when export exceeds the row ceiling', async () => {
      setSession();
      vi.mocked(axios.get).mockImplementation(async (url: string) => {
        if (url.endsWith('/me/access')) return { data: { permissions: ['ticket.read'] } };
        if (url.endsWith('/tickets/export.csv')) return Promise.reject({ response: { status: 413 } });
        if (url.endsWith('/tickets')) return { data: { items: [ticket()], has_more: false, count: 1, limit: 20 } };
        return Promise.reject({ response: { status: 404 } });
      });

      renderAt(<TicketsPage />);
      const { fireEvent } = await import('@testing-library/react');
      fireEvent.click(await screen.findByRole('button', { name: /Exportar CSV/ }));

      expect(await screen.findByText('A exportação excede 5000 tickets. Refine os filtros e tente novamente.')).toBeInTheDocument();
    });
  });
});
