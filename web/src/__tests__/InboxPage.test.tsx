import { describe, it, expect, beforeEach, vi } from 'vitest';
import { render, screen, waitFor } from '@testing-library/react';
import { QueryClient, QueryClientProvider } from '@tanstack/react-query';
import axios from 'axios';
import { InboxPage } from '../pages/InboxPage';

vi.mock('axios');
vi.mock('../hooks/useRealtimeEvents', () => ({
  useRealtimeEvents: vi.fn(),
}));

const mockAxios = axios as any;

describe('InboxPage', () => {
  let queryClient: QueryClient;

  beforeEach(() => {
    queryClient = new QueryClient({
      defaultOptions: { queries: { retry: false } },
    });
    vi.clearAllMocks();
    localStorage.setItem('tenantId', 'tenant-a-uuid');
  });

  it('renders conversation list', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: {
        items: [
          {
            id: 'conv-1',
            contact_name: 'Alice',
            contact_phone: '+5511999999999',
            status: 'active',
            updated_at: new Date().toISOString(),
            message_count: 5,
            unread_count: 2,
          },
        ],
        has_more: false,
      },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <InboxPage />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(screen.getByText('Alice')).toBeInTheDocument();
      expect(screen.getByText('+5511999999999')).toBeInTheDocument();
    });
  });

  it('handles pagination cursor', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: {
        items: [
          {
            id: 'conv-1',
            contact_name: 'Bob',
            contact_phone: '+5511988888888',
            status: 'closed',
            updated_at: new Date().toISOString(),
            message_count: 10,
            unread_count: 0,
          },
        ],
        has_more: true,
        next_cursor: 'cursor-token-xxx',
      },
    });

    const { rerender } = render(
      <QueryClientProvider client={queryClient}>
        <InboxPage />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(screen.getByText('Bob')).toBeInTheDocument();
    });

    // Load more button should exist
    const loadMoreBtn = screen.getByText('Load More');
    expect(loadMoreBtn).toBeInTheDocument();
  });

  it('displays empty state', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: {
        items: [],
        has_more: false,
      },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <InboxPage />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(screen.getByText('No conversations yet')).toBeInTheDocument();
    });
  });

  it('includes tenant_id in request URL', async () => {
    mockAxios.get.mockResolvedValueOnce({
      data: { items: [], has_more: false },
    });

    render(
      <QueryClientProvider client={queryClient}>
        <InboxPage />
      </QueryClientProvider>
    );

    await waitFor(() => {
      expect(mockAxios.get).toHaveBeenCalledWith(
        expect.stringContaining('tenant-a-uuid'),
        expect.any(Object)
      );
    });
  });
});
