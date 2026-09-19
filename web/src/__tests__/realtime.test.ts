import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest';
import { renderHook } from '@testing-library/react';
import { readEvents, useRealtimeEvents } from '../hooks/useRealtimeEvents';

const enc = new TextEncoder();
function stream(chunks: string[]): ReadableStream<Uint8Array> {
  return new ReadableStream({
    start(c) {
      chunks.forEach((x) => c.enqueue(enc.encode(x)));
      c.close();
    },
  });
}

describe('readEvents (SSE parser)', () => {
  it('joins data lines, ignores comments and handles frames split across chunks', async () => {
    const got: string[] = [];
    await readEvents(stream([': keepalive\n\ndata: {"a"', ':1}\n\ndata: line1\ndata: line2\n\n']), (d) => got.push(d));
    expect(got).toEqual(['{"a":1}', 'line1\nline2']);
  });

  it('accepts CRLF frames', async () => {
    const got: string[] = [];
    await readEvents(stream(['data: x\r\n\r\n']), (d) => got.push(d));
    expect(got).toEqual(['x']);
  });
});

describe('useRealtimeEvents', () => {
  beforeEach(() => {
    localStorage.clear();
    localStorage.setItem('token', 'tok');
  });
  afterEach(() => {
    vi.unstubAllGlobals();
  });

  it('sends the Authorization header (never a token in the URL) and delivers parsed events', async () => {
    const fetchMock = vi.fn().mockResolvedValue({ ok: true, status: 200, body: stream(['data: {"type":"message_received","id":"m1"}\n\n']) });
    vi.stubGlobal('fetch', fetchMock);
    const events: any[] = [];
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 't1', conversationId: 'c1', onEvent: (e) => events.push(e) }));
    await vi.waitFor(() => expect(events).toHaveLength(1));
    const [url, init] = fetchMock.mock.calls[0];
    expect(url).toBe('/api/v1/tenants/t1/inbox/conversations/c1/events');
    expect(url).not.toContain('tok');
    expect(init.headers.Authorization).toBe('Bearer tok');
    expect(events[0]).toMatchObject({ type: 'message_received', id: 'm1' });
    unmount();
  });

  it('does not reconnect when only the callbacks change identity', async () => {
    const fetchMock = vi.fn().mockImplementation(() => new Promise(() => {})); // stays connected
    vi.stubGlobal('fetch', fetchMock);
    const { rerender, unmount } = renderHook(({ cb }) => useRealtimeEvents({ tenantId: 't1', onEvent: cb }), { initialProps: { cb: () => {} } });
    rerender({ cb: () => {} });
    rerender({ cb: () => {} });
    expect(fetchMock).toHaveBeenCalledTimes(1);
    unmount();
  });

  it('does nothing without a tenant', () => {
    const fetchMock = vi.fn();
    vi.stubGlobal('fetch', fetchMock);
    renderHook(() => useRealtimeEvents({ tenantId: '', onEvent: () => {} }));
    expect(fetchMock).not.toHaveBeenCalled();
  });

  it('reconnects with backoff after a failure and reports the error', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn()
      .mockRejectedValueOnce(new Error('boom'))
      .mockResolvedValue({ ok: true, status: 200, body: stream(['data: {"type":"x","id":"1"}\n\n']) });
    vi.stubGlobal('fetch', fetchMock);
    const errors: Error[] = [];
    const events: any[] = [];
    let reconnects = 0;
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 't1', onEvent: (e) => events.push(e), onError: (e) => errors.push(e), onReconnect: () => { reconnects += 1; } }));
    await vi.advanceTimersByTimeAsync(1500);
    expect(errors[0]?.message).toBe('boom');
    expect(fetchMock.mock.calls.length).toBeGreaterThanOrEqual(2);
    expect(events).toHaveLength(1);
    expect(reconnects).toBe(0); // the very first successful connection is not a reconnect
    unmount();
    vi.useRealTimers();
  });

  it('calls onReconnect after a dropped stream is re-established', async () => {
    vi.useFakeTimers();
    const fetchMock = vi.fn()
      .mockResolvedValueOnce({ ok: true, status: 200, body: stream([]) }) // connects, then the stream ends (drop)
      .mockResolvedValue({ ok: true, status: 200, body: new ReadableStream({ start() {} }) }); // stays open
    vi.stubGlobal('fetch', fetchMock);
    let reconnects = 0;
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 't1', onEvent: () => {}, onReconnect: () => { reconnects += 1; } }));
    await vi.advanceTimersByTimeAsync(1500);
    expect(fetchMock.mock.calls.length).toBe(2);
    expect(reconnects).toBe(1);
    unmount();
    vi.useRealTimers();
  });
});
