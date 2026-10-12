import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import axios from 'axios';
import Sidebar from '../components/Sidebar';
import { waitingAcross } from '../hooks/useHubWaiting';
import { renderAt, setSession } from './testUtils';
import { clearActing, setActing } from '../lib/acting';

// The menu item "Conversas" carries the number of conversations waiting for an answer in the instances served through the Hubs (the badge of the approved
// layout, docs/ux/HUB-LAYOUT-CONTRACT.md). Display only: the number is what the server counted for this person.
vi.mock('axios');

const hub = (id: string) => ({ id, name: 'Hub ' + id, role: 'hub_agent' });
let hubsList = [hub('h1')];
let companiesByHub: Record<string, { id: string; name: string; unanswered?: number }[]> = {};
const inboxCalls: Record<string, unknown>[] = [];

function serve() {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    const u = String(url);
    if (u.endsWith('/hubs')) return { data: { items: hubsList } };
    const m = u.match(/\/hubs\/([^/]+)\/inbox$/);
    if (m) {
      inboxCalls.push({ ...(config?.params ?? {}) });
      return { data: { items: [], has_more: false, count: 0, limit: 1, companies: companiesByHub[m[1]!] ?? [] } };
    }
    return Promise.reject({ response: { status: 404 } });
  });
}

beforeEach(() => {
  vi.resetAllMocks();
  localStorage.clear();
  sessionStorage.clear();
  clearActing();
  setSession();
  hubsList = [hub('h1')];
  companiesByHub = {};
  inboxCalls.length = 0;
});
afterEach(() => clearActing());

const conversationsLink = () => screen.getAllByRole('link').find((l) => l.getAttribute('href') === '/inbox')!;

describe('Sidebar — the number of conversations waiting, on "Conversas"', () => {
  it('shows the sum of what the server counted per instance, in the name of the link too', async () => {
    companiesByHub = { h1: [{ id: 'a', name: 'A', unanswered: 3 }, { id: 'b', name: 'B', unanswered: 4 }] };
    serve();
    renderAt(<Sidebar />);
    await waitFor(() => expect(conversationsLink()).toHaveAttribute('aria-label', 'Conversas, 7 aguardando resposta'));
    expect(conversationsLink().textContent).toContain('7');
    // it asks only for the counters, one conversation at most, and names no company
    expect(inboxCalls[0]).toMatchObject({ include: 'company_counts', limit: 1 });
    expect(JSON.stringify(inboxCalls[0])).not.toMatch(/tenant/);
  });

  it('counts an instance served by two Hubs once', () => {
    expect(waitingAcross([[{ id: 'a', name: 'A', unanswered: 3 }], [{ id: 'a', name: 'A', unanswered: 3 }, { id: 'b', name: 'B', unanswered: 2 }]])).toBe(5);
    expect(waitingAcross([undefined, []])).toBe(0);
  });

  it('shows nothing when nothing waits, 99+ when it is a lot, and never when attending a single instance', async () => {
    companiesByHub = { h1: [{ id: 'a', name: 'A', unanswered: 0 }] };
    serve();
    const first = renderAt(<Sidebar />);
    await waitFor(() => expect(inboxCalls.length).toBeGreaterThan(0));
    expect(conversationsLink()).not.toHaveAttribute('aria-label');
    first.unmount();

    companiesByHub = { h1: [{ id: 'a', name: 'A', unanswered: 250 }] };
    renderAt(<Sidebar />);
    await waitFor(() => expect(conversationsLink()).toHaveAttribute('aria-label', 'Conversas, 250 aguardando resposta'));
    expect(conversationsLink().textContent).toContain('99+');
  });

  it('is not asked while the session acts for one instance (the menu offers only the conversations of that instance there)', async () => {
    companiesByHub = { h1: [{ id: 'a', name: 'A', unanswered: 9 }] };
    serve();
    setActing('h1', 'Instância A');
    renderAt(<Sidebar />);
    await screen.findAllByRole('link');
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled());
    expect(inboxCalls).toHaveLength(0);
    expect(conversationsLink()).not.toHaveAttribute('aria-label');
  });

  it('forgets the number when the read starts failing (access revoked) instead of showing the last true number', async () => {
    companiesByHub = { h1: [{ id: 'a', name: 'A', unanswered: 9 }] };
    serve();
    const { queryClient } = (await import('./testUtils')).renderAtWithClient(<Sidebar />);
    await waitFor(() => expect(conversationsLink()).toHaveAttribute('aria-label', 'Conversas, 9 aguardando resposta'));
    // the Hub stops answering (revoked): the cached 9 must not stay on the menu
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/hubs')) return { data: { items: hubsList } };
      return Promise.reject({ response: { status: 404 } });
    });
    await queryClient.refetchQueries({ queryKey: ['hub-companies-probe'] });
    await waitFor(() => expect(conversationsLink()).not.toHaveAttribute('aria-label'));
  });
});
