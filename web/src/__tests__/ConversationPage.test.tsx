import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import axios from 'axios';
import { ConversationPage } from '../pages/ConversationPage';

vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: vi.fn(),
}));

const mockAxios = axios as any;

describe('ConversationPage', () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    vi.clearAllMocks();
    localStorage.setItem('tenantId', 'tenant-a-uuid');
  });

  it('renders messages list', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: {
        items: [
          {
            id: 'msg-1',
            conversation_id: 'conv-1',
            body: 'Hello there',
            direction: 'inbound',
            status: 'delivered',
            created_at: new Date().toISOString(),
          },
        ],
        has_more: false,
      },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <ConversationPage conversationId="conv-1" />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(screen.getByText('Hello there')).toBeInTheDocument();
    });
  });

  it('includes tenant_id and conversation_id in request', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: { items: [], has_more: false },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <ConversationPage conversationId="conv-xyz" />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(mockAxios.get).toHaveBeenCalledWith(
        expect.stringContaining('tenant-a-uuid'),
        expect.any(Object)
      );
      expect(mockAxios.get).toHaveBeenCalledWith(
        expect.stringContaining('conv-xyz'),
        expect.any(Object)
      );
    });
  });

  it('displays message metadata', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: {
        items: [
          {
            id: 'msg-2',
            conversation_id: 'conv-1',
            body: 'Test message',
            direction: 'outbound',
            status: 'sent',
            created_at: new Date().toISOString(),
          },
        ],
        has_more: false,
      },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <ConversationPage conversationId="conv-1" />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(screen.getByText('Test message')).toBeInTheDocument();
      expect(screen.getByText('sent')).toBeInTheDocument();
    });
  });

  it('renders message input form', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: { items: [], has_more: false },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <ConversationPage conversationId="conv-1" />
      </QueryClientProvider>
    );

    await waitFor(() => {
      const input = screen.getByPlaceholderText('Type a message...');
      expect(input).toBeInTheDocument();
      expect(screen.getByText('Send')).toBeInTheDocument();
    });
  });
});

describe('ConversationPage sending', () => {
  const renderPage = () => {
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    return render(
      <QueryClientProvider client={queryClient}>
        <ConversationPage conversationId="conv-1" />
      </QueryClientProvider>
    );
  };

  beforeEach(() => {
    vi.resetAllMocks();
    localStorage.setItem('tenantId', 'tenant-a-uuid');
    localStorage.setItem('token', 'tok');
    mockAxios.get.mockResolvedValue({ data: { items: [], has_more: false } });
  });

  it('posts to the real endpoint with auth and an Idempotency-Key, then shows the queued message', async () => {
    mockAxios.post.mockResolvedValueOnce({
      data: { id: 'm-1', conversation_id: 'conv-1', body: 'Olá', direction: 'outbound', status: 'queued', created_at: new Date().toISOString() },
    });
    renderPage();
    await userEvent.type(screen.getByPlaceholderText('Type a message...'), 'Olá');
    await userEvent.click(screen.getByText('Send'));
    await waitFor(() => expect(screen.getByText('queued')).toBeInTheDocument());
    const [url, body, config] = mockAxios.post.mock.calls[0];
    expect(url).toBe('http://localhost:8080/api/v1/tenants/tenant-a-uuid/inbox/conversations/conv-1/messages');
    expect(body).toEqual({ text: 'Olá' });
    expect(config.headers.Authorization).toBe('Bearer tok');
    expect(config.headers['Idempotency-Key']).toMatch(/^[A-Za-z0-9._:-]{8,128}$/);
    expect((screen.getByPlaceholderText('Type a message...') as HTMLInputElement).value).toBe('');
  });

  it('keeps the text and reuses the same Idempotency-Key when retrying after a failure', async () => {
    mockAxios.post
      .mockRejectedValueOnce({ response: { status: 409, data: 'conversation must be assigned before replying' } })
      .mockResolvedValueOnce({ data: { id: 'm-2', conversation_id: 'conv-1', body: 'Oi', direction: 'outbound', status: 'queued', created_at: new Date().toISOString() } });
    renderPage();
    await userEvent.type(screen.getByPlaceholderText('Type a message...'), 'Oi');
    await userEvent.click(screen.getByText('Send'));
    expect(await screen.findByRole('alert')).toHaveTextContent('Assign this conversation to yourself before replying');
    expect((screen.getByPlaceholderText('Type a message...') as HTMLInputElement).value).toBe('Oi');
    await userEvent.click(screen.getByText('Send'));
    await waitFor(() => expect(mockAxios.post).toHaveBeenCalledTimes(2));
    expect(mockAxios.post.mock.calls[1][2].headers['Idempotency-Key']).toBe(mockAxios.post.mock.calls[0][2].headers['Idempotency-Key']);
    await waitFor(() => expect(screen.queryByRole('alert')).toBeNull());
  });

  it('does not call the API for a blank message', async () => {
    renderPage();
    await userEvent.type(screen.getByPlaceholderText('Type a message...'), '   ');
    await userEvent.click(screen.getByText('Send'));
    expect(mockAxios.post).not.toHaveBeenCalled();
  });
});
