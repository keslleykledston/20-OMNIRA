import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useRealtimeEvents } from '../hooks/useRealtimeEvents'
import { clearActing, setActing } from '../lib/acting'

// Attending through a Hub (ADR-0040 phase 05) the stream opens like any other, declaring the acting context for the instance being attended and for no other.
describe('useRealtimeEvents while acting for a hub', () => {
  const fetchMock = vi.fn()
  beforeEach(() => {
    fetchMock.mockReset()
    fetchMock.mockImplementation(() => new Promise(() => undefined)) // a stream that never answers
    vi.stubGlobal('fetch', fetchMock)
    localStorage.clear()
  })
  afterEach(() => {
    clearActing()
    vi.unstubAllGlobals()
  })

  it('opens the stream normally', () => {
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 'T-A', onEvent: () => undefined }))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0][0])).toContain('/tenants/T-A/inbox/events')
    unmount()
  })

  it('opens the stream with the acting context while attending the instance', () => {
    localStorage.setItem('tenantId', 'T-B')
    setActing('hub-1', 'Beta')
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 'T-B', conversationId: 'c1', onEvent: () => undefined }))
    expect(fetchMock).toHaveBeenCalledTimes(1)
    expect(String(fetchMock.mock.calls[0][0])).toContain('/tenants/T-B/inbox/conversations/c1/events')
    expect(fetchMock.mock.calls[0][1].headers['X-Omnira-Acting-As']).toBe('hub:hub-1')
    unmount()
  })

  it('never declares the acting context for another instance, nor when not acting', () => {
    localStorage.setItem('tenantId', 'T-B')
    setActing('hub-1', 'Beta')
    const other = renderHook(() => useRealtimeEvents({ tenantId: 'T-OTHER', onEvent: () => undefined }))
    expect(fetchMock.mock.calls[0][1].headers['X-Omnira-Acting-As']).toBeUndefined()
    other.unmount()
    clearActing()
    fetchMock.mockClear()
    const plain = renderHook(() => useRealtimeEvents({ tenantId: 'T-B', onEvent: () => undefined }))
    expect(fetchMock.mock.calls[0][1].headers['X-Omnira-Acting-As']).toBeUndefined()
    plain.unmount()
  })

  it('stops retrying when the server refuses the delegated stream (access withdrawn)', async () => {
    vi.useFakeTimers()
    localStorage.setItem('tenantId', 'T-B')
    setActing('hub-1', 'Beta')
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({ status: 404, ok: false, body: null })
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 'T-B', onEvent: () => undefined }))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(fetchMock).toHaveBeenCalledTimes(1)
    unmount()
    vi.useRealTimers()
  })

  it('keeps retrying a refused stream for an instance it does not act on (no acting header was sent)', async () => {
    vi.useFakeTimers()
    localStorage.setItem('tenantId', 'T-B')
    setActing('hub-1', 'Beta')
    fetchMock.mockReset()
    fetchMock.mockResolvedValue({ status: 404, ok: false, body: null })
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 'T-OTHER', onEvent: () => undefined }))
    await vi.advanceTimersByTimeAsync(10_000)
    expect(fetchMock.mock.calls.length).toBeGreaterThan(1)
    unmount()
    vi.useRealTimers()
  })
})
