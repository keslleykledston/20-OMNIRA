import { useEffect, useRef } from 'react';
import { API_BASE } from '../lib/config';
import { authHeaders, handleUnauthorized } from '../lib/session';
import { readEvents } from './useRealtimeEvents';

const MAX_BACKOFF_MS = 30000;
const MIN_GAP_MS = 1000;

/**
 * The change signal of the aggregated Hub tab (`GET /hubs/{id}/events`). The stream carries NO content: each frame only says "something changed in an
 * instance you serve", and the screen answers by re-reading its lists through the normal API, which decides what the person may see. It reconnects with
 * backoff, stops for good on 401 (sign-out) and on 403/404 (the access is gone: the screen's own reads show the notice), and tells the caller to refetch
 * after a drop (events may have been missed). The slow poll of the screen stays as the safety net.
 */
export function useHubEvents(hubId: string, onChanged: () => void, enabled = true) {
  const onChangedRef = useRef(onChanged);
  useEffect(() => {
    onChangedRef.current = onChanged;
  });

  useEffect(() => {
    if (!hubId || !enabled) return;
    const controller = new AbortController();
    let stopped = false;
    let timer: ReturnType<typeof setTimeout> | undefined;
    let last = 0;
    // at most one refetch per MIN_GAP_MS, however many frames arrive
    const changed = () => {
      const wait = last + MIN_GAP_MS - Date.now();
      if (wait <= 0) {
        last = Date.now();
        onChangedRef.current();
      } else if (!timer) {
        timer = setTimeout(() => {
          timer = undefined;
          last = Date.now();
          if (!stopped) onChangedRef.current();
        }, wait);
      }
    };
    const run = async () => {
      let attempt = 0;
      let dropped = false;
      while (!stopped) {
        try {
          const res = await fetch(`${API_BASE}/hubs/${encodeURIComponent(hubId)}/events`, {
            headers: { ...authHeaders(), Accept: 'text/event-stream' },
            signal: controller.signal,
          });
          if (res.status === 401) {
            handleUnauthorized();
            return;
          }
          if (res.status === 403 || res.status === 404) return; // the access is gone (or the signal is not mounted): nothing to retry
          if (!res.ok || !res.body) throw new Error(`events connection failed (HTTP ${res.status})`);
          attempt = 0;
          if (dropped) changed();
          dropped = true;
          await readEvents(res.body, () => changed());
        } catch {
          if (stopped) return;
        }
        if (stopped) return;
        await sleep(Math.min(1000 * 2 ** attempt, MAX_BACKOFF_MS), controller.signal);
        attempt += 1;
      }
    };
    void run();
    return () => {
      stopped = true;
      if (timer) clearTimeout(timer);
      controller.abort();
    };
  }, [hubId, enabled]);
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
