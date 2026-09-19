import { describe, it, expect, beforeEach, vi } from 'vitest';
import { screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import axios from 'axios';
import { ConversationPage } from '../pages/ConversationPage';
import { renderAt, mockGets, setSession, TENANT } from './testUtils';

vi.mock('axios');
let onEvent: ((e: any) => void) | undefined;
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: (opts: any) => {
    onEvent = opts.onEvent;
  },
}));

const CONV = 'conv-1';
const conversation = (over: object = {}) => ({
  id: CONV, contact_name: 'Alice', contact_phone: '+5511999999999', status: 'open', ...over,
});
const message = (over: object = {}) => ({
  id: 'm-1', conversation_id: CONV, body: 'Hello there', direction: 'inbound', status: 'delivered',
  created_at: new Date().toISOString(), ...over,
});
const page = () => renderAt(<ConversationPage conversationId={CONV} />, `/inbox/${CONV}`, '/inbox/:id');

describe('ConversationPage', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
    onEvent = undefined;
  });

  it('loads the conversation header and the messages with the JWT', async () => {
    mockGets({
      [`/inbox/conversations/${CONV}`]: conversation(),
      [`/inbox/conversations/${CONV}/messages`]: { items: [message()], has_more: false },
    });
    page();
    expect(await screen.findByText('Alice')).toBeInTheDocument();
    expect(await screen.findByText('Hello there')).toBeInTheDocument();
    expect(screen.getByText('+5511999999999')).toBeInTheDocument();
    const urls = vi.mocked(axios.get).mock.calls.map((c) => c[0]);
    expect(urls).toContain(`/api/v1/tenants/${TENANT}/inbox/conversations/${CONV}`);
    expect(urls).toContain(`/api/v1/tenants/${TENANT}/inbox/conversations/${CONV}/messages`);
    expect((vi.mocked(axios.get).mock.calls[0] as any[])[1].headers.Authorization).toBe('Bearer tok');
  });

  it('shows the assignee state from the conversation (Release when assigned)', async () => {
    mockGets({
      [`/inbox/conversations/${CONV}`]: conversation({ assigned_to_user_id: 'user-1234567890' }),
      [`/inbox/conversations/${CONV}/messages`]: { items: [], has_more: false },
    });
    page();
    expect(await screen.findByText('Release')).toBeInTheDocument();
  });

  it('refetches on realtime events: new message appears once, status and assignee update', async () => {
    const base = {
      [`/inbox/conversations/${CONV}`]: conversation(),
      [`/inbox/conversations/${CONV}/messages`]: { items: [message({ id: 'm-1', status: 'sent', direction: 'outbound' })], has_more: false },
    };
    mockGets(base);
    page();
    await screen.findByText('sent');
    mockGets({
      [`/inbox/conversations/${CONV}`]: conversation({ assigned_to_user_id: 'user-1234567890' }),
      [`/inbox/conversations/${CONV}/messages`]: {
        items: [message({ id: 'm-2', body: 'live one', status: 'received', created_at: '2999-01-01T00:00:00Z' }), message({ id: 'm-1', status: 'delivered', direction: 'outbound' })],
        has_more: false,
      },
    });
    onEvent!({ type: 'message_received', id: CONV, timestamp: '', data: { message_id: 'm-2' } });
    onEvent!({ type: 'message_received', id: CONV, timestamp: '', data: { message_id: 'm-2' } });
    onEvent!({ type: 'conversation_updated', id: CONV, timestamp: '', data: { assigned_to_user_id: 'user-1234567890' } });
    await screen.findByText('live one');
    expect(screen.getAllByText('live one')).toHaveLength(1);
    expect(await screen.findByText('delivered')).toBeInTheDocument(); // status_changed via refetch
    expect(screen.queryByText('sent')).toBeNull();
    expect(await screen.findByText('Release')).toBeInTheDocument(); // assignee via refetch
  });

  it('goes back to the inbox', async () => {
    mockGets({
      [`/inbox/conversations/${CONV}`]: conversation(),
      [`/inbox/conversations/${CONV}/messages`]: { items: [], has_more: false },
    });
    page();
    await userEvent.click(await screen.findByText('← Inbox'));
    expect(await screen.findByTestId('elsewhere')).toBeInTheDocument();
  });
});

describe('ConversationPage sending', () => {
  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.clear();
    setSession();
    mockGets({
      [`/inbox/conversations/${CONV}`]: conversation(),
      [`/inbox/conversations/${CONV}/messages`]: { items: [], has_more: false },
    });
  });

  it('posts to the real endpoint with auth and an Idempotency-Key, then shows the queued message', async () => {
    vi.mocked(axios.post).mockResolvedValueOnce({
      data: { id: 'm-1', conversation_id: CONV, body: 'Olá', direction: 'outbound', status: 'queued', created_at: new Date().toISOString() },
    });
    page();
    await userEvent.type(await screen.findByPlaceholderText('Type a message...'), 'Olá');
    await userEvent.click(screen.getByText('Send'));
    await waitFor(() => expect(screen.getByText('queued')).toBeInTheDocument());
    const [url, body, config] = vi.mocked(axios.post).mock.calls[0] as any[];
    expect(url).toBe(`/api/v1/tenants/${TENANT}/inbox/conversations/${CONV}/messages`);
    expect(body).toEqual({ text: 'Olá' });
    expect(config.headers.Authorization).toBe('Bearer tok');
    expect(config.headers['Idempotency-Key']).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    expect((screen.getByPlaceholderText('Type a message...') as HTMLInputElement).value).toBe('');
  });

  it('keeps the text and reuses the same Idempotency-Key when retrying after a failure', async () => {
    vi.mocked(axios.post)
      .mockRejectedValueOnce({ response: { status: 409, data: 'conversation must be assigned before replying' } })
      .mockResolvedValueOnce({ data: { id: 'm-2', conversation_id: CONV, body: 'Oi', direction: 'outbound', status: 'queued', created_at: new Date().toISOString() } });
    page();
    await userEvent.type(await screen.findByPlaceholderText('Type a message...'), 'Oi');
    await userEvent.click(screen.getByText('Send'));
    expect(await screen.findByRole('alert')).toHaveTextContent('Assign this conversation to yourself before replying');
    expect((screen.getByPlaceholderText('Type a message...') as HTMLInputElement).value).toBe('Oi');
    await userEvent.click(screen.getByText('Send'));
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(2));
    const calls = vi.mocked(axios.post).mock.calls as any[];
    expect(calls[1][2].headers['Idempotency-Key']).toBe(calls[0][2].headers['Idempotency-Key']);
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  });

  it('does not call the API for a blank message', async () => {
    page();
    await userEvent.type(await screen.findByPlaceholderText('Type a message...'), '   ');
    await userEvent.click(screen.getByText('Send'));
    expect(axios.post).not.toHaveBeenCalled();
  });
});
