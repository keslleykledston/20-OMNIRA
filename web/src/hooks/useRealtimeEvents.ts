import { useEffect, useRef } from 'react';
import { RealtimeEvent } from '../types/api';
import { API_BASE } from '../lib/config';
import { authHeaders, handleUnauthorized } from '../lib/session';
import { isActing } from '../lib/acting';

interface UseRealtimeEventsOptions {
  tenantId: string;
  conversationId?: string;
  onEvent: (event: RealtimeEvent) => void;
  onError?: (error: Error) => void;
  /** Called after the stream is re-established following a drop: events may have been missed, refetch. */
  onReconnect?: () => void;
}

const MAX_BACKOFF_MS = 15000;

/**
 * M05.2 realtime (SSE) subscription.
 * Uses fetch streaming instead of EventSource because EventSource cannot send the
 * Authorization header (putting the JWT in the URL would leak it into logs). Reconnects
 * with exponential backoff and does not resubscribe when callback identities change.
 */
export function useRealtimeEvents({ tenantId, conversationId, onEvent, onError, onReconnect }: UseRealtimeEventsOptions) {
  const onEventRef = useRef(onEvent);
  const onErrorRef = useRef(onError);
  const onReconnectRef = useRef(onReconnect);
  useEffect(() => {
    onEventRef.current = onEvent;
    onErrorRef.current = onError;
    onReconnectRef.current = onReconnect;
  });

  useEffect(() => {
    if (!tenantId) return;
    // Attending through a Hub: the realtime streams are not part of the delegated context yet (ADR-0040 phase 05), the screen polls instead.
    if (isActing()) return;
    const url = conversationId
      ? `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/events`
      : `${API_BASE}/tenants/${tenantId}/inbox/events`;
    const controller = new AbortController();
    let stopped = false;

    const run = async () => {
      let attempt = 0;
      let dropped = false;
      while (!stopped) {
        try {
          const res = await fetch(url, {
            headers: { ...authHeaders(), Accept: 'text/event-stream' },
            signal: controller.signal,
          });
          if (res.status === 401) {
            handleUnauthorized();
            return;
          }
          if (!res.ok || !res.body) throw new Error(`SSE connection failed (HTTP ${res.status})`);
          attempt = 0;
          if (dropped) onReconnectRef.current?.();
          dropped = true; // any later exit of this loop iteration is a drop
          await readEvents(res.body, (data) => {
            try {
              const parsed = JSON.parse(data);
              if (parsed && parsed.error) throw new Error(String(parsed.error));
              onEventRef.current(parsed as RealtimeEvent);
            } catch (error) {
              onErrorRef.current?.(new Error(`Failed to handle event: ${data}`));
            }
          });
        } catch (error) {
          if (stopped) return;
          onErrorRef.current?.(error as Error);
        }
        if (stopped) return;
        await sleep(Math.min(1000 * 2 ** attempt, MAX_BACKOFF_MS), controller.signal);
        attempt += 1;
      }
    };
    void run();

    return () => {
      stopped = true;
      controller.abort();
    };
  }, [tenantId, conversationId]);
}

// Minimal SSE parser: events are separated by a blank line; "data:" lines are joined; ":" lines are comments.
export async function readEvents(body: ReadableStream<Uint8Array>, onData: (data: string) => void): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  for (;;) {
    const { done, value } = await reader.read();
    if (done) return;
    buffer += decoder.decode(value, { stream: true }).replace(/\r\n/g, '\n');
    let boundary: number;
    while ((boundary = buffer.indexOf('\n\n')) >= 0) {
      const frame = buffer.slice(0, boundary);
      buffer = buffer.slice(boundary + 2);
      const data = frame
        .split('\n')
        .filter((line) => line.startsWith('data:'))
        .map((line) => line.slice(5).replace(/^ /, ''))
        .join('\n');
      if (data) onData(data);
    }
  }
}

function sleep(ms: number, signal: AbortSignal): Promise<void> {
  return new Promise((resolve) => {
    const timer = setTimeout(resolve, ms);
    signal.addEventListener('abort', () => {
      clearTimeout(timer);
      resolve();
    }, { once: true });
  });
}
