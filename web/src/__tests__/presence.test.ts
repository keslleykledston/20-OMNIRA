import { describe, it, expect, beforeEach, afterEach, vi } from 'vitest'
import { renderHook } from '@testing-library/react'
import axios from 'axios'
import { getPresenceSessionId, presenceAPI } from '../lib/presence'
import { usePresenceHeartbeat } from '../hooks/usePresenceHeartbeat'

describe('getPresenceSessionId', () => {
  beforeEach(() => sessionStorage.clear())

  it('generates one id per tab and keeps it stable across calls', () => {
    const first = getPresenceSessionId()
    const second = getPresenceSessionId()
    expect(first).toBe(second)
    expect(first).toMatch(/^[0-9a-f-]{36}$/i)
  })

  it('never reuses localStorage: a fresh tab (fresh sessionStorage) gets a new id', () => {
    const a = getPresenceSessionId()
    sessionStorage.clear() // simulates a new tab
    const b = getPresenceSessionId()
    expect(a).not.toBe(b)
  })
})

describe('presenceAPI.heartbeat', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('token', 'tok')
    localStorage.setItem('tenantId', 't1')
    sessionStorage.clear()
  })
  afterEach(() => { vi.restoreAllMocks() })

  it('never sends tenant_id, membership_id, agent_profile_id or user_id in the payload', async () => {
    const postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
    await presenceAPI.heartbeat()
    expect(postSpy).toHaveBeenCalledTimes(1)
    const [url, body] = postSpy.mock.calls[0]
    expect(url).toMatch(/\/tenants\/t1\/me\/presence\/heartbeat$/)
    expect(Object.keys(body as object)).toEqual(['session_id'])
  })

  it('treats a 403 (no active AgentProfile) as a quiet no-op, not an error', async () => {
    vi.spyOn(axios, 'post').mockRejectedValue({ isAxiosError: true, response: { status: 403 } })
    vi.spyOn(axios, 'isAxiosError').mockReturnValue(true)
    await expect(presenceAPI.heartbeat()).resolves.toBe(false)
  })
})

describe('usePresenceHeartbeat', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('token', 'tok')
    localStorage.setItem('tenantId', 't1')
    sessionStorage.clear()
    vi.useFakeTimers()
  })
  afterEach(() => {
    vi.useRealTimers()
    vi.restoreAllMocks()
  })

  it('sends one heartbeat immediately, then again every 30s while mounted', async () => {
    const postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
    renderHook(() => usePresenceHeartbeat(true))
    await vi.advanceTimersByTimeAsync(0)
    expect(postSpy).toHaveBeenCalledTimes(1)
    await vi.advanceTimersByTimeAsync(30_000)
    expect(postSpy).toHaveBeenCalledTimes(2)
  })

  it('sends nothing when disabled', async () => {
    const postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
    renderHook(() => usePresenceHeartbeat(false))
    await vi.advanceTimersByTimeAsync(60_000)
    expect(postSpy).not.toHaveBeenCalled()
  })

  it('stops heartbeating after unmount', async () => {
    const postSpy = vi.spyOn(axios, 'post').mockResolvedValue({ data: {} })
    const { unmount } = renderHook(() => usePresenceHeartbeat(true))
    await vi.advanceTimersByTimeAsync(0)
    unmount()
    await vi.advanceTimersByTimeAsync(60_000)
    expect(postSpy).toHaveBeenCalledTimes(1)
  })
})
