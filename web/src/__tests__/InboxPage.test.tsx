import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { InboxPage } from '../pages/InboxPage';
import { renderAt, mockGets, setSession, TENANT } from './testUtils';

vi.mock('axios');
let onEvent: ((e: any) => void) | undefined;
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: (opts: any) => {
    onEvent = opts.onEvent;
  },
}));

const conv = (over: object = {}) => ({
  id: 'conv-1', contact_name: 'Alice', contact_phone: '+5511999999999', status: 'open',
  updated_at: new Date().toISOString(), ...over,
});

describe('InboxPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
    onEvent = undefined;
  });

  it('lists conversations from the same-origin API with the JWT', async () => {
    mockGets({ '/inbox/conversations': { items: [conv()], has_more: false } });
    renderAt(<InboxPage />, '/inbox', '/inbox');
    expect(await screen.findByText('Alice')).toBeInTheDocument();
    expect(screen.getByText('+5511999999999')).toBeInTheDocument();
    const [url, config] = vi.mocked(axios.get).mock.calls[0] as any[];
    expect(url).toBe(`/api/v1/tenants/${TENANT}/inbox/conversations`);
    expect(config.headers.Authorization).toBe('Bearer tok');
  });

  it('opens the conversation when an item is clicked', async () => {
    mockGets({ '/inbox/conversations': { items: [conv()], has_more: false } });
    renderAt(<InboxPage />, '/inbox', '/inbox');
    await userEvent.click(await screen.findByText('Alice'));
    expect(await screen.findByTestId('elsewhere')).toBeInTheDocument(); // navigated to /inbox/conv-1
  });

  it('does not call the API without a tenant in the session', async () => {
    localStorage.removeItem('tenantId');
    mockGets({});
    renderAt(<InboxPage />, '/inbox', '/inbox');
    await waitFor(() => expect(screen.getByText('No conversations yet')).toBeInTheDocument());
    expect(axios.get).not.toHaveBeenCalled();
  });

  it('paginates with the cursor', async () => {
    mockGets({ '/inbox/conversations': { items: [conv()], has_more: true, next_cursor: 'abc' } });
    renderAt(<InboxPage />, '/inbox', '/inbox');
    await userEvent.click(await screen.findByText('Load More'));
    await waitFor(() => {
      const calls = vi.mocked(axios.get).mock.calls as any[];
      expect(calls.some((c) => c[1]?.params?.cursor === 'abc')).toBe(true);
    });
  });

  it('refetches the list when a realtime event arrives (new message / assignment)', async () => {
    mockGets({ '/inbox/conversations': { items: [conv()], has_more: false } });
    renderAt(<InboxPage />, '/inbox', '/inbox');
    await screen.findByText('Alice');
    mockGets({ '/inbox/conversations': { items: [conv(), conv({ id: 'conv-2', contact_name: 'Bruno', created_at: '2999-01-01T00:00:00Z' })], has_more: false } });
    onEvent!({ type: 'message_received', id: 'conv-2', timestamp: '', data: {} });
    expect(await screen.findByText('Bruno')).toBeInTheDocument();
    expect(screen.getByText('Alice')).toBeInTheDocument();
  });
});
