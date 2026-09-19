import { useEffect, useCallback } from 'react';
import { RealtimeEvent } from '../types/api';

const API_BASE = 'http://localhost:8080/api/v1';

interface UseRealtimeEventsOptions {
  tenantId: string;
  conversationId?: string;
  onEvent: (event: RealtimeEvent) => void;
  onError?: (error: Error) => void;
}

/**
 * Hook for M05.2 SSE realtime events
 * Subscribes to inbox or per-conversation events
 */
export function useRealtimeEvents({
  tenantId,
  conversationId,
  onEvent,
  onError,
}: UseRealtimeEventsOptions) {
  useEffect(() => {
    if (!tenantId) return;

    const endpoint = conversationId
      ? `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/events`
      : `${API_BASE}/tenants/${tenantId}/inbox/events`;

    const eventSource = new EventSource(endpoint);

    const handleMessage = (e: MessageEvent) => {
      try {
        const event = JSON.parse(e.data) as RealtimeEvent;
        onEvent(event);
      } catch (error) {
        onError?.(new Error(`Failed to parse event: ${e.data}`));
      }
    };

    const handleError = () => {
      const error = new Error('SSE connection failed');
      onError?.(error);
      eventSource.close();
    };

    eventSource.addEventListener('message', handleMessage);
    eventSource.addEventListener('error', handleError);

    return () => {
      eventSource.removeEventListener('message', handleMessage);
      eventSource.removeEventListener('error', handleError);
      eventSource.close();
    };
  }, [tenantId, conversationId, onEvent, onError]);
}
