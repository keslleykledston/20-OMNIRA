import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import { useRealtimeEvents } from '../hooks/useRealtimeEvents'
import { clearActing, setActing } from '../lib/acting'

// Attending through a Hub the realtime streams are not part of the delegated context yet (ADR-0040 phase 05): the screen polls, it must not open them.
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

  it('does not open any stream while acting', () => {
    setActing('hub-1', 'Beta')
    const { unmount } = renderHook(() => useRealtimeEvents({ tenantId: 'T-B', conversationId: 'c1', onEvent: () => undefined }))
    expect(fetchMock).not.toHaveBeenCalled()
    unmount()
  })
})
