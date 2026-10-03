import { beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import {
  DEFAULT_INBOX_SETTINGS,
  inboxSettingsAPI,
  inboxSettingsErrorMessage,
  toThresholds,
  validateWaitThresholds,
} from '../lib/inboxSettings'
import { waitInfo } from '../lib/inboxModel'
import { setSession } from './testUtils'

vi.mock('axios')

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('validateWaitThresholds (same rules as the API)', () => {
  it('accepts a valid pair, including both boundaries', () => {
    expect(validateWaitThresholds(30, 120)).toBeNull()
    expect(validateWaitThresholds(1, 2)).toBeNull()
    expect(validateWaitThresholds(1, 10080)).toBeNull()
  })
  it('rejects zero or negative attention, and non-integers', () => {
    expect(validateWaitThresholds(0, 10)).toMatch(/inteiros/)
    expect(validateWaitThresholds(-1, 10)).toMatch(/inteiros/)
    expect(validateWaitThresholds(1.5, 10)).toMatch(/inteiros/)
    expect(validateWaitThresholds(Number.NaN, 10)).toMatch(/inteiros/)
  })
  it('requires critical to be greater than attention, and at most a week', () => {
    expect(validateWaitThresholds(60, 60)).toMatch(/maior/)
    expect(validateWaitThresholds(60, 30)).toMatch(/maior/)
    expect(validateWaitThresholds(30, 10081)).toMatch(/10080/)
  })
})

describe('defaults and mapping', () => {
  it('the standard values are the 30 min / 2 h the list always used', () => {
    expect(DEFAULT_INBOX_SETTINGS).toEqual({ wait_warn_minutes: 30, wait_danger_minutes: 120 })
    expect(toThresholds(DEFAULT_INBOX_SETTINGS)).toEqual({ warnMinutes: 30, dangerMinutes: 120 })
  })
})

describe('waitInfo with tenant thresholds', () => {
  const now = new Date(2026, 9, 3, 15, 0)
  const minAgo = (m: number) => new Date(now.getTime() - m * 60_000).toISOString()
  it('uses the thresholds it is given for the tone', () => {
    const strict = { warnMinutes: 5, dangerMinutes: 10 }
    expect(waitInfo(minAgo(4), now, strict)?.tone).toBe('muted')
    expect(waitInfo(minAgo(5), now, strict)?.tone).toBe('warning')
    expect(waitInfo(minAgo(10), now, strict)?.tone).toBe('danger')
    const lax = { warnMinutes: 240, dangerMinutes: 480 }
    expect(waitInfo(minAgo(200), now, lax)?.tone).toBe('muted')
    expect(waitInfo(minAgo(300), now, lax)?.tone).toBe('warning')
  })
  it('falls back to 30 min / 2 h without thresholds', () => {
    expect(waitInfo(minAgo(29), now)?.tone).toBe('muted')
    expect(waitInfo(minAgo(30), now)?.tone).toBe('warning')
    expect(waitInfo(minAgo(120), now)?.tone).toBe('danger')
  })
})

describe('inboxSettingsAPI', () => {
  it('reads and writes the tenant settings route with the session headers', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: { wait_warn_minutes: 45, wait_danger_minutes: 180 } })
    vi.mocked(axios.put).mockResolvedValue({ data: { wait_warn_minutes: 10, wait_danger_minutes: 20 } })
    expect(await inboxSettingsAPI.get()).toEqual({ wait_warn_minutes: 45, wait_danger_minutes: 180 })
    expect(vi.mocked(axios.get).mock.calls[0][0]).toMatch(/\/tenants\/[^/]+\/settings\/inbox$/)
    expect(await inboxSettingsAPI.update({ wait_warn_minutes: 10, wait_danger_minutes: 20 })).toEqual({ wait_warn_minutes: 10, wait_danger_minutes: 20 })
    const [url, body] = vi.mocked(axios.put).mock.calls[0]
    expect(url).toMatch(/\/settings\/inbox$/)
    expect(body).toEqual({ wait_warn_minutes: 10, wait_danger_minutes: 20 }) // never a tenant_id in the body
  })
  it('turns API refusals into sentences the user can act on', () => {
    expect(inboxSettingsErrorMessage({ response: { status: 403 } })).toMatch(/administradores/)
    expect(inboxSettingsErrorMessage({ response: { status: 422 } })).toMatch(/crítico/)
    expect(inboxSettingsErrorMessage({ response: { status: 500 } })).toMatch(/Tente novamente/)
    expect(inboxSettingsErrorMessage(new Error('boom'))).toMatch(/Tente novamente/)
  })
})
