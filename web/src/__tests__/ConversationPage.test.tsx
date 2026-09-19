import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
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
