import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { act, renderHook } from '@testing-library/react';
import { useHubEvents } from '../hooks/useHubEvents';

// The change signal of the aggregated Hub tab: a frame means "re-read now", nothing more. It must coalesce bursts, reconnect after a drop (and refetch, since events
// may have been missed), and stop for good when the access is gone or the person signed out.
const enc = new TextEncoder();

function sseResponse() {
  let controller!: ReadableStreamDefaultController<Uint8Array>;
  const body = new ReadableStream<Uint8Array>({ start: (c) => (controller = c) });
  return { res: { status: 200, ok: true, body } as unknown as Response, push: (text: string) => controller.enqueue(enc.encode(text)), end: () => controller.close() };
}
const tick = async (ms = 0) => {
  await act(async () => {
    await vi.advanceTimersByTimeAsync(ms);
  });
};

beforeEach(() => {
  vi.useFakeTimers();
  localStorage.clear();
});
afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe('useHubEvents', () => {
  it('asks for the stream of ITS hub only, tells the caller to refetch on a frame, and coalesces a burst', async () => {
    const s = sseResponse();
    const fetchMock = vi.fn().mockResolvedValue(s.res);
    vi.stubGlobal('fetch', fetchMock);
    const onChanged = vi.fn();
    renderHook(() => useHubEvents('hub-1', onChanged));
    await tick();
    expect(String(fetchMock.mock.calls[0][0])).toMatch(/\/hubs\/hub-1\/events$/);
    expect(fetchMock.mock.calls[0][1].headers.Accept).toBe('text/event-stream');
    s.push('retry: 5000\n: connected\n\n');
    await tick();
    expect(onChanged).not.toHaveBeenCalled(); // comments and retry are not changes
    for (let i = 0; i < 20; i++) s.push('event: changed\ndata: {}\n\n');
    await tick();
    expect(onChanged).toHaveBeenCalledTimes(1); // the first at once
    await tick(1200);
    expect(onChanged).toHaveBeenCalledTimes(2); // and ONE more for the rest of the burst, not nineteen
  });

  it('reconnects after a drop with backoff and refetches once, since events may have been missed', async () => {
    const first = sseResponse();
    const second = sseResponse();
    const fetchMock = vi.fn().mockResolvedValueOnce(first.res).mockResolvedValueOnce(second.res);
    vi.stubGlobal('fetch', fetchMock);
    const onChanged = vi.fn();
    renderHook(() => useHubEvents('hub-1', onChanged));
    await tick();
    first.end();
    await tick();
    expect(fetchMock).toHaveBeenCalledTimes(1); // it waits before asking again
    await tick(1100);
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(onChanged).toHaveBeenCalledTimes(1); // the refetch after the drop
  });

  it('stops for good when the access is gone (403/404) and does not retry', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ status: 404, ok: false, body: null } as unknown as Response);
    vi.stubGlobal('fetch', fetchMock);
    renderHook(() => useHubEvents('hub-1', vi.fn()));
    await tick(60000);
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });

  it('does nothing while disabled or without a hub, and closes the stream on unmount', async () => {
    const s = sseResponse();
    const fetchMock = vi.fn().mockResolvedValue(s.res);
    vi.stubGlobal('fetch', fetchMock);
    renderHook(() => useHubEvents('', vi.fn()));
    renderHook(() => useHubEvents('hub-1', vi.fn(), false));
    await tick();
    expect(fetchMock).not.toHaveBeenCalled();
    const live = renderHook(() => useHubEvents('hub-1', vi.fn()));
    await tick();
    const signal = fetchMock.mock.calls[0][1].signal as AbortSignal;
    expect(signal.aborted).toBe(false);
    live.unmount();
    expect(signal.aborted).toBe(true);
  });

  it('keeps backing off when a proxy answers 200 and closes at once (a connection that never carried anything does not reset the delay)', async () => {
    const closed = () => {
      const r = sseResponse();
      r.end();
      return r.res;
    };
    const fetchMock = vi.fn().mockImplementation(async () => closed());
    vi.stubGlobal('fetch', fetchMock);
    renderHook(() => useHubEvents('hub-1', vi.fn()));
    await tick(); // 1st connection, closed at once
    expect(fetchMock).toHaveBeenCalledTimes(1);
    await tick(1100); // after 1 s: the 2nd
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await tick(1500); // the delay doubled to 2 s: still waiting
    expect(fetchMock).toHaveBeenCalledTimes(2);
    await tick(700);
    expect(fetchMock).toHaveBeenCalledTimes(3);
  });

  it('reads once more when the access is refused, so the screen shows the truth at once', async () => {
    vi.stubGlobal('fetch', vi.fn().mockResolvedValue({ status: 404, ok: false, body: null } as unknown as Response));
    const onChanged = vi.fn();
    renderHook(() => useHubEvents('hub-1', onChanged));
    await tick();
    expect(onChanged).toHaveBeenCalledTimes(1);
  });
});
