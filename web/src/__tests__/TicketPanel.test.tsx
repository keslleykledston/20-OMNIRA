import { beforeEach, describe, expect, it, vi } from 'vitest';
import { screen } from '@testing-library/react';
import axios from 'axios';
import { TicketPanel } from '../components/TicketPanel';
import { renderAt, setSession } from './testUtils';

// PRODUCT.6-B: no real ERP ticketing connector is wired into production/pilot
// runtime. TicketPanel must check GET .../ticket on mount and render an
// honest unavailable state on 503 — never the create form, never a fake
// ticket. This replaces trust in MockCRMConnector as proof of behavior.
vi.mock('axios');

beforeEach(() => {
  vi.clearAllMocks();
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/integrations/companies')) return { data: { items: [] } };
    return Promise.reject({ response: { status: 404 } });
  });
});

describe('TicketPanel', () => {
  it('shows the honest unavailable state on 503 and never renders the create form', async () => {
    setSession();
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/integrations/companies')) return { data: { items: [] } };
      if (url.endsWith('/ticket')) return Promise.reject({ response: { status: 503 } });
      return Promise.reject({ response: { status: 404 } });
    });

    renderAt(<TicketPanel conversationId="c-1" />);

    expect(await screen.findByText('Chamados no ERP não configurados')).toBeInTheDocument();
    expect(screen.queryByLabelText('Novo chamado')).not.toBeInTheDocument();
  });

  it('keeps the create form available when the ticketing integration itself is reachable', async () => {
    setSession();
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/integrations/companies')) return { data: { items: [] } };
      if (url.endsWith('/ticket')) return { data: {} };
      return Promise.reject({ response: { status: 404 } });
    });

    renderAt(<TicketPanel conversationId="c-1" />);

    expect(await screen.findByLabelText('Novo chamado')).toBeInTheDocument();
    expect(screen.queryByText('Chamados no ERP não configurados')).not.toBeInTheDocument();
  });
});
