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
  // PRODUCT.6-D (ADR-0013): every real ticket today is local-only — no
  // connector is wired for any tenant (PRODUCT.6-B). Explicit null here
  // (not omitted) mirrors exactly what the real backend now always sends.
  provider: null,
  external_ticket_id: null,
  external_status: null,
  external_status_label: null,
  sync_status: null,
  last_synced_at: null,
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

  // PRODUCT.6-D (ADR-0013): honest origin labeling, no fake K3G/IXC/SGP
  // projections in runtime data — only a test fixture explicitly sets
  // provider/external_ticket_id here.
  describe('ticket origin', () => {
    it('shows a "Local" badge for a legacy ticket with no provider', async () => {
      setSession();
      mockGets({
        '/tickets': { items: [ticket({ subject: 'Legacy one' })], has_more: false, count: 1, limit: 20 },
      });
      renderAt(<TicketsPage />);

      await screen.findAllByText('Legacy one');
      expect(screen.getAllByText('Local').length).toBeGreaterThan(0);
    });

    it('shows the real provider and external ticket ID for a projected ticket', async () => {
      setSession();
      mockGets({
        '/tickets': {
          items: [ticket({ subject: 'External one', provider: 'k3g_crm', external_ticket_id: '28176' })],
          has_more: false,
          count: 1,
          limit: 20,
        },
      });
      renderAt(<TicketsPage />);

      await screen.findAllByText('External one');
      expect(screen.getAllByText('k3g_crm').length).toBeGreaterThan(0);
      expect(screen.getAllByText('#28176').length).toBeGreaterThan(0);
      expect(screen.queryByText('Local')).not.toBeInTheDocument();
    });

    it('shows the provider badge without an ID suffix when external_ticket_id is absent', async () => {
      setSession();
      mockGets({
        '/tickets': {
          items: [ticket({ subject: 'Provider only', provider: 'ixc' })],
          has_more: false,
          count: 1,
          limit: 20,
        },
      });
      renderAt(<TicketsPage />);

      await screen.findAllByText('Provider only');
      expect(screen.getAllByText('ixc').length).toBeGreaterThan(0);
      expect(screen.queryByText(/^#/)).not.toBeInTheDocument();
    });
  });

  // PRODUCT.6-O2D: exact external_ticket_id search against the local
  // projection only — no provider call, no free-text search.
  describe('external ticket ID search', () => {
    // A
    it('renders a labeled search input for external ticket ID', async () => {
      setSession();
      mockGets({ '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 } });
      renderAt(<TicketsPage />);
      await screen.findAllByText('Preciso de ajuda com meu pedido');
      expect(screen.getByLabelText('ID do chamado externo')).toBeInTheDocument();
    });

    // B
    it('submitting a search sends external_ticket_id to the canonical list endpoint', async () => {
      setSession();
      mockGets({
        '/tickets': {
          items: [ticket({ subject: 'External one', provider: 'k3g', external_ticket_id: '28182' })],
          has_more: false,
          count: 1,
          limit: 20,
        },
      });
      renderAt(<TicketsPage />);
      await screen.findAllByText('External one');

      const { fireEvent } = await import('@testing-library/react');
      const input = screen.getByLabelText('ID do chamado externo');
      fireEvent.change(input, { target: { value: '28182' } });
      fireEvent.submit(input.closest('form')!);

      await screen.findAllByText('External one');
      const call = [...vi.mocked(axios.get).mock.calls].reverse().find(([url]) => (url as string).endsWith('/tickets'));
      expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ external_ticket_id: '28182' }) });
    });

    // C
    it('clearing the search restores the unfiltered list (no external_ticket_id param)', async () => {
      setSession();
      mockGets({ '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 } });
      renderAt(<TicketsPage />);
      await screen.findAllByText('Preciso de ajuda com meu pedido');

      const { fireEvent } = await import('@testing-library/react');
      const input = screen.getByLabelText('ID do chamado externo');
      fireEvent.change(input, { target: { value: '28182' } });
      fireEvent.submit(input.closest('form')!);
      await vi.waitFor(() => {
        const call = [...vi.mocked(axios.get).mock.calls].reverse().find(([url]) => (url as string).endsWith('/tickets'));
        expect(call?.[1]).toMatchObject({ params: expect.objectContaining({ external_ticket_id: '28182' }) });
      });

      fireEvent.click(screen.getByLabelText('Limpar pesquisa'));
      await vi.waitFor(() => {
        const call = [...vi.mocked(axios.get).mock.calls].reverse().find(([url]) => (url as string).endsWith('/tickets'));
        expect(call?.[1]?.params).not.toHaveProperty('external_ticket_id');
      });
    });

    // D
    it('renders the matching ticket for a found external ID', async () => {
      setSession();
      mockGets({
        '/tickets': {
          items: [ticket({ subject: 'Found ticket', provider: 'k3g', external_ticket_id: '28182' })],
          has_more: false,
          count: 1,
          limit: 20,
        },
      });
      renderAt(<TicketsPage />);
      const { fireEvent } = await import('@testing-library/react');
      await screen.findAllByText('Found ticket');
      const input = screen.getByLabelText('ID do chamado externo');
      fireEvent.change(input, { target: { value: '28182' } });
      fireEvent.submit(input.closest('form')!);
      expect((await screen.findAllByText('Found ticket')).length).toBeGreaterThan(0);
    });

    // E
    it('shows a search-specific empty state when no ticket matches the external ID', async () => {
      setSession();
      mockGets({ '/tickets': { items: [], has_more: false, count: 0, limit: 20 } });
      renderAt(<TicketsPage />);
      const { fireEvent } = await import('@testing-library/react');
      await screen.findByText('Nenhum ticket encontrado');
      const input = screen.getByLabelText('ID do chamado externo');
      fireEvent.change(input, { target: { value: 'does-not-exist' } });
      fireEvent.submit(input.closest('form')!);
      expect(await screen.findByText('Nenhum chamado com esse ID externo')).toBeInTheDocument();
    });

    // PRODUCT.6-O2D2 section 13: conversation navigation from a ticket row.
    describe('Abrir conversa navigation', () => {
      // A, B
      it('a row with conversation_id shows Abrir conversa, linking to the frozen deep-link contract', async () => {
        setSession();
        mockGets({
          '/tickets': {
            items: [ticket({ subject: 'Linked ticket', conversation_id: 'c-123' })],
            has_more: false,
            count: 1,
            limit: 20,
          },
        });
        renderAt(<TicketsPage />);
        await screen.findAllByText('Linked ticket');

        const links = screen.getAllByRole('link', { name: 'Abrir conversa' });
        expect(links.length).toBeGreaterThan(0);
        expect(links[0]).toHaveAttribute('href', '/inbox?conversation_id=c-123');
      });

      // C
      it('a row without a usable conversation_id renders no broken action', async () => {
        setSession();
        mockGets({
          '/tickets': {
            items: [ticket({ subject: 'No conversation', conversation_id: '' })],
            has_more: false,
            count: 1,
            limit: 20,
          },
        });
        renderAt(<TicketsPage />);
        await screen.findAllByText('No conversation');
        expect(screen.queryByRole('link', { name: 'Abrir conversa' })).not.toBeInTheDocument();
      });

      // D
      it('external ID search result still shows a working Abrir conversa action', async () => {
        setSession();
        mockGets({
          '/tickets': {
            items: [ticket({ subject: 'Found via search', conversation_id: 'c-999', provider: 'k3g', external_ticket_id: '28182' })],
            has_more: false,
            count: 1,
            limit: 20,
          },
        });
        renderAt(<TicketsPage />);
        const { fireEvent } = await import('@testing-library/react');
        await screen.findAllByText('Found via search');
        const input = screen.getByLabelText('ID do chamado externo');
        fireEvent.change(input, { target: { value: '28182' } });
        fireEvent.submit(input.closest('form')!);
        await screen.findAllByText('Found via search');

        const links = screen.getAllByRole('link', { name: 'Abrir conversa' });
        expect(links[0]).toHaveAttribute('href', '/inbox?conversation_id=c-999');
      });

      // E
      it('never calls a mutation endpoint when the conversation action is present', async () => {
        setSession();
        mockGets({
          '/tickets': {
            items: [ticket({ subject: 'Linked ticket', conversation_id: 'c-123' })],
            has_more: false,
            count: 1,
            limit: 20,
          },
        });
        renderAt(<TicketsPage />);
        await screen.findAllByText('Linked ticket');
        expect(vi.mocked(axios.post)).not.toHaveBeenCalled();
        expect(vi.mocked(axios.patch)).not.toHaveBeenCalled();
        expect(vi.mocked(axios.delete)).not.toHaveBeenCalled();
      });
    });

    // I
    it('never calls a mutation endpoint from the search flow', async () => {
      setSession();
      mockGets({ '/tickets': { items: [ticket()], has_more: false, count: 1, limit: 20 } });
      renderAt(<TicketsPage />);
      const { fireEvent } = await import('@testing-library/react');
      await screen.findAllByText('Preciso de ajuda com meu pedido');
      const input = screen.getByLabelText('ID do chamado externo');
      fireEvent.change(input, { target: { value: '28182' } });
      fireEvent.submit(input.closest('form')!);
      await vi.waitFor(() => {});
      expect(vi.mocked(axios.post)).not.toHaveBeenCalled();
      expect(vi.mocked(axios.patch)).not.toHaveBeenCalled();
      expect(vi.mocked(axios.delete)).not.toHaveBeenCalled();
    });
  });
});
