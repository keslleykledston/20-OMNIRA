import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import ConversationsEntry from '../pages/ConversationsEntry';
import { MemoryRouter } from 'react-router-dom';
import { render } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import { renderAt } from './testUtils';

vi.mock('axios');
const switchTenant = vi.hoisted(() => vi.fn());
const enterDelegatedInstance = vi.hoisted(() => vi.fn());
const leaveDelegatedInstance = vi.hoisted(() => vi.fn());
vi.mock('../lib/tenants', async (orig) => ({ ...(await orig<typeof import('../lib/tenants')>()), switchTenant, enterDelegatedInstance, leaveDelegatedInstance }));
// The two screens the tabs host are tested on their own; here they are markers.
vi.mock('../pages/InboxWorkspace', () => ({ default: () => <div>Caixa completa da instância</div> }));
vi.mock('../pages/HubInboxPage', () => ({
  default: (p: { hubId: string; embedded?: boolean; onlyCompany?: string; notice?: string }) => (
    <div data-testid="hub-inbox">
      hub:{p.hubId} embedded:{String(!!p.embedded)} only:{p.onlyCompany ?? ''}
      {p.notice && <p>{p.notice}</p>}
    </div>
  ),
}));

let mine: { id: string; legal_name: string; trade_name?: string }[] = [];
let companies: { id: string; name: string; full_context?: boolean }[] = [];
let hubs: { id: string; name: string; role: string }[] = [{ id: 'hub-1', name: 'K3G', role: 'hub_agent' }];
let probeFails = false;

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    const u = String(url);
    if (u.endsWith('/hubs')) return { data: { items: hubs } };
    if (u.endsWith('/tenants')) return { data: mine };
    if (u.includes('/hubs/') && u.endsWith('/inbox')) {
      if (probeFails) return Promise.reject({ response: { status: 500 } });
      return { data: { items: [], companies, has_more: false, count: 0, limit: 1 } };
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

const T1 = { id: 'T1', legal_name: 'Alfa Ltda', trade_name: 'Alfa' };
const T2 = { id: 'T2', legal_name: 'Beta Ltda', trade_name: 'Beta' };

beforeEach(() => {
  localStorage.clear();
  sessionStorage.clear();
  localStorage.setItem('tenantId', 'T1');
  localStorage.setItem('token', 'tok');
  mine = [T1];
  companies = [];
  hubs = [{ id: 'hub-1', name: 'K3G', role: 'hub_agent' }];
  probeFails = false;
  switchTenant.mockReset();
  enterDelegatedInstance.mockReset();
  leaveDelegatedInstance.mockReset();
  serve();
});
afterEach(() => {
  vi.useRealTimers();
});

const tabs = () => screen.queryAllByRole('tab').map((t) => t.textContent);

describe('Conversas tabs', () => {
  it('shows no tabs for a person with a single instance (the classic workspace, unchanged)', async () => {
    renderAt(<ConversationsEntry />);
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
    expect(screen.queryByRole('tablist')).toBeNull();
  });

  it('two own instances and no Hub: one tab each, the current one open, no "Todas"', async () => {
    mine = [T1, T2];
    hubs = [];
    renderAt(<ConversationsEntry />);
    expect(await screen.findByRole('tablist', { name: 'Instâncias' })).toBeInTheDocument();
    expect(tabs()).toEqual(['Alfa', 'Beta']);
    expect(screen.getByRole('tab', { name: 'Alfa' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByText('Caixa completa da instância')).toBeInTheDocument();
  });

  it('a Hub serving two or more instances adds "Todas", opened first, as the embedded unified inbox', async () => {
    mine = [T1];
    companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub' }];
    renderAt(<ConversationsEntry />);
    await waitFor(() => expect(tabs()).toEqual(['Todas', 'Alfa', 'Beta Hub']));
    expect(screen.getByRole('tab', { name: 'Todas' })).toHaveAttribute('aria-selected', 'true');
    expect(screen.getByTestId('hub-inbox')).toHaveTextContent('hub:hub-1 embedded:true only:');
  });

  it('an instance reached only through the Hub opens the Hub view pinned to that instance, with the reduced-view notice', async () => {
    mine = [T1];
    companies = [{ id: 'B', name: 'Beta Hub' }];
    renderAt(<ConversationsEntry />, '/inbox?instancia=B');
    expect(await screen.findByTestId('hub-inbox')).toHaveTextContent('hub:hub-1 embedded:true only:B');
    expect(screen.getByText(/Mídia, dados do cliente e chamado no ERP chegam na próxima etapa/)).toBeInTheDocument();
    expect(screen.getByRole('tab', { name: 'Beta Hub' })).toHaveAttribute('aria-selected', 'true');
  });

  it('clicking another instance of one\'s own switches the session instance and lands back on the tab', async () => {
    mine = [T1, T2];
    hubs = [];
    renderAt(<ConversationsEntry />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Beta' }));
    expect(switchTenant).toHaveBeenCalledTimes(1);
    expect(switchTenant.mock.calls[0][0]).toBe('T2');
    // the callback is the navigation target
    const assign = vi.fn();
    const original = window.location;
    Object.defineProperty(window, 'location', { configurable: true, value: { ...original, assign } });
    (switchTenant.mock.calls[0][1] as () => void)();
    expect(assign).toHaveBeenCalledWith('/inbox?instancia=T2');
    Object.defineProperty(window, 'location', { configurable: true, value: original });
  });

  it('clicking a Hub-only instance does not switch the session instance', async () => {
    mine = [T1];
    companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub' }];
    renderAt(<ConversationsEntry />);
    await userEvent.click(await screen.findByRole('tab', { name: 'Beta Hub' }));
    expect(switchTenant).not.toHaveBeenCalled();
    expect(await screen.findByTestId('hub-inbox')).toHaveTextContent('only:B');
  });

  it('when the session is on an instance that is no longer the person\'s, it moves once to the first one that is', async () => {
    mine = [T1, T2];
    hubs = [];
    localStorage.setItem('tenantId', 'GONE');
    renderAt(<ConversationsEntry />);
    await waitFor(() => expect(switchTenant).toHaveBeenCalledTimes(1));
    expect(switchTenant.mock.calls[0][0]).toBe('T1');
  });

  it('does not keep switching if the first switch did not take (no reload loop): offers a button instead', async () => {
    mine = [T1, T2];
    hubs = [];
    localStorage.setItem('tenantId', 'GONE');
    sessionStorage.setItem('omnira.conversas.navTried', 'switch:T1');
    renderAt(<ConversationsEntry />);
    expect(await screen.findByRole('button', { name: 'Abrir a instância' })).toBeInTheDocument();
    expect(switchTenant).not.toHaveBeenCalled();
  });

  it('an instance the person has no access to (stale link) shows the lost-access notice and renders nothing of an instance', async () => {
    mine = [T1, T2];
    hubs = [];
    renderAt(<ConversationsEntry />, '/inbox?instancia=NOT-MINE');
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem mais acesso a esta instância');
    expect(screen.getByText(/Fale com o administrador do Hub/)).toBeInTheDocument();
    expect(screen.queryByText('Caixa completa da instância')).toBeNull();
    expect(screen.queryByTestId('hub-inbox')).toBeNull();
  });

  it('access that ends while the tab is open: the next read replaces the tab with the notice and drops the instance\'s cache', async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    mine = [T1];
    companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub' }];
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(
      <QueryClientProvider client={client}>
        <MemoryRouter initialEntries={['/inbox?instancia=B']}>
          <ConversationsEntry />
        </MemoryRouter>
      </QueryClientProvider>,
    );
    expect(await screen.findByTestId('hub-inbox')).toBeInTheDocument();
    // what the open tab had cached (keys carry the instance id, like the real ones), plus another instance's data that must stay
    client.setQueryData(['inbox-conversations', 'B', 'all', ''], { secret: 'conversas de B' });
    client.setQueryData(['inbox-conversations', 'T1', 'all', ''], { keep: true });
    // the Hub revokes B
    companies = [{ id: 'T1', name: 'Alfa' }];
    await vi.advanceTimersByTimeAsync(16_000);
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeNull(), { timeout: 3000 });
    expect(screen.getByRole('alert')).toHaveTextContent('Você não tem mais acesso a esta instância');
    expect(screen.queryByTestId('hub-inbox')).toBeNull();
    expect(tabs()).not.toContain('Beta Hub');
    await waitFor(() => expect(client.getQueryData(['inbox-conversations', 'B', 'all', ''])).toBeUndefined());
    expect(client.getQueryData(['inbox-conversations', 'T1', 'all', ''])).toEqual({ keep: true });
  });

  it('a failed read is not taken as a lost access', async () => {
    mine = [T1, T2];
    companies = [{ id: 'T1', name: 'Alfa' }, { id: 'T2', name: 'Beta' }];
    probeFails = true;
    renderAt(<ConversationsEntry />, '/inbox?instancia=B');
    await waitFor(() => expect(screen.queryByText('Carregando conversas…')).toBeNull());
    expect(screen.queryByRole('alert')).toBeNull();
  });

  it('?modo=empresa still opens the classic workspace without tabs (old links)', async () => {
    mine = [T1, T2];
    renderAt(<ConversationsEntry />, '/inbox?modo=empresa');
    expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
    expect(screen.queryByRole('tablist')).toBeNull();
  });

  describe('attending an instance through the Hub with the full context (ADR-0040)', () => {
    const hubName = 'hub-1';
    beforeEach(() => {
      mine = [T1];
      companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub', full_context: true }];
    });

    it('clicking such an instance enters the delegated context (a full navigation), not the text view', async () => {
      renderAt(<ConversationsEntry />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Beta Hub' }));
      expect(enterDelegatedInstance).toHaveBeenCalledWith('B', hubName, 'Beta Hub');
      expect(switchTenant).not.toHaveBeenCalled();
    });

    it('an instance WITHOUT the full context keeps the text view and never enters the delegated context', async () => {
      companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub', full_context: false }];
      renderAt(<ConversationsEntry />);
      await userEvent.click(await screen.findByRole('tab', { name: 'Beta Hub' }));
      expect(enterDelegatedInstance).not.toHaveBeenCalled();
      expect(await screen.findByTestId('hub-inbox')).toHaveTextContent('only:B');
    });

    it('a link to such an instance enters the context once on its own', async () => {
      renderAt(<ConversationsEntry />, '/inbox?instancia=B');
      await waitFor(() => expect(enterDelegatedInstance).toHaveBeenCalledTimes(1));
      expect(enterDelegatedInstance).toHaveBeenCalledWith('B', hubName, 'Beta Hub');
    });

    it('does not keep entering if the first entry did not take (no reload loop): offers a button instead', async () => {
      sessionStorage.setItem('omnira.conversas.navTried', 'enter:B');
      renderAt(<ConversationsEntry />, '/inbox?instancia=B');
      expect(await screen.findByRole('button', { name: 'Abrir a instância' })).toBeInTheDocument();
      expect(enterDelegatedInstance).not.toHaveBeenCalled();
    });

    it('once the session acts for the hub on that instance, the SAME workspace opens (not the text view)', async () => {
      localStorage.setItem('tenantId', 'B');
      localStorage.setItem('actingHub', hubName);
      localStorage.setItem('actingName', 'Beta Hub');
      renderAt(<ConversationsEntry />, '/inbox?instancia=B');
      expect(await screen.findByText('Caixa completa da instância')).toBeInTheDocument();
      expect(screen.queryByTestId('hub-inbox')).toBeNull();
      expect(enterDelegatedInstance).not.toHaveBeenCalled();
      expect(leaveDelegatedInstance).not.toHaveBeenCalled();
    });

    it('while acting, a member tab switches to the person\'s own instance (which also drops the acting context)', async () => {
      localStorage.setItem('tenantId', 'B');
      localStorage.setItem('actingHub', hubName);
      renderAt(<ConversationsEntry />, '/inbox?instancia=B');
      await screen.findByText('Caixa completa da instância');
      await userEvent.click(screen.getByRole('tab', { name: 'Alfa' }));
      expect(switchTenant).toHaveBeenCalledWith('T1', expect.any(Function));
    });

    it('a stale acting context on one of the person\'s OWN instances is dropped by switching to it (the session never acts for a hub on an own instance)', async () => {
      mine = [T1, T2];
      companies = [];
      hubs = [];
      localStorage.setItem('tenantId', 'T1');
      localStorage.setItem('actingHub', hubName);
      renderAt(<ConversationsEntry />, '/inbox?instancia=T1');
      await waitFor(() => expect(switchTenant).toHaveBeenCalledTimes(1));
      expect(switchTenant.mock.calls[0][0]).toBe('T1');
    });

    it('while acting, leaving for "Todas" or a text-only instance drops the acting context once', async () => {
      companies = [{ id: 'T1', name: 'Alfa' }, { id: 'B', name: 'Beta Hub', full_context: true }, { id: 'C', name: 'Gama', full_context: false }];
      localStorage.setItem('tenantId', 'B');
      localStorage.setItem('actingHub', hubName);
      renderAt(<ConversationsEntry />, '/inbox?instancia=C');
      await waitFor(() => expect(leaveDelegatedInstance).toHaveBeenCalledTimes(1));
      expect(leaveDelegatedInstance).toHaveBeenCalledWith([expect.objectContaining({ id: 'T1' })]);
    });

    it('a delegated session whose access ends shows the notice and drops nothing else it should keep', async () => {
      vi.useFakeTimers({ shouldAdvanceTime: true });
      localStorage.setItem('tenantId', 'B');
      localStorage.setItem('actingHub', hubName);
      renderAt(<ConversationsEntry />, '/inbox?instancia=B');
      await screen.findByText('Caixa completa da instância');
      companies = [{ id: 'T1', name: 'Alfa' }];
      await vi.advanceTimersByTimeAsync(16_000);
      await waitFor(() => expect(screen.queryByRole('alert')).not.toBeNull(), { timeout: 3000 });
      expect(screen.getByRole('alert')).toHaveTextContent('Você não tem mais acesso a esta instância');
      expect(screen.queryByText('Caixa completa da instância')).toBeNull();
    });
  });
});
