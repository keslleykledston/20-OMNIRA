import { beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import { ACTING_HEADER, clearActing, getActingHub, getActingName, installActingInterceptor, isActing, messageMediaUrl, setActing } from '../lib/acting'
import { clearSession } from '../lib/session'
import { enterDelegatedInstance, leaveDelegatedInstance, switchTenant } from '../lib/tenants'

// The acting context (ADR-0040): declared per request for the ONE instance the session acts in; it grants nothing, the server decides.
describe('acting context', () => {
  beforeEach(() => {
    localStorage.clear()
    localStorage.setItem('tenantId', 'T-B')
  })

  it('is off by default and can be set and cleared', () => {
    expect(isActing()).toBe(false)
    setActing('hub-1', 'Beta')
    expect(isActing()).toBe(true)
    expect(getActingHub()).toBe('hub-1')
    expect(getActingName()).toBe('Beta')
    clearActing()
    expect(isActing()).toBe(false)
    expect(getActingName()).toBe('')
  })

  it('the interceptor adds the header only to requests for the instance the session acts in', async () => {
    installActingInterceptor()
    installActingInterceptor() // idempotent
    setActing('hub-1', 'Beta')
    const seen: Record<string, unknown>[] = []
    const adapter = (config: { url?: string; headers: { get: (k: string) => unknown } }) => {
      seen.push({ url: config.url, header: config.headers.get(ACTING_HEADER) })
      return Promise.resolve({ data: {}, status: 200, statusText: 'OK', headers: {}, config })
    }
    await axios.get('/api/v1/tenants/T-B/inbox/conversations', { adapter: adapter as never })
    await axios.get('/api/v1/tenants/T-OTHER/inbox/conversations', { adapter: adapter as never })
    await axios.get('/api/v1/hubs', { adapter: adapter as never })
    expect(seen).toEqual([
      { url: '/api/v1/tenants/T-B/inbox/conversations', header: 'hub:hub-1' },
      { url: '/api/v1/tenants/T-OTHER/inbox/conversations', header: undefined },
      { url: '/api/v1/hubs', header: undefined },
    ])
    clearActing()
    seen.length = 0
    await axios.get('/api/v1/tenants/T-B/inbox/conversations', { adapter: adapter as never })
    expect(seen[0].header).toBeUndefined()
  })

  it('files load from the tenant route normally and from the hub path (no header possible on <img>) while acting', () => {
    expect(messageMediaUrl('T-B', 'M1')).toBe('/api/v1/tenants/T-B/messages/M1/media')
    setActing('hub-1', 'Beta')
    expect(messageMediaUrl('T-B', 'M1')).toBe('/api/v1/hubs/hub-1/serve/T-B/messages/M1/media')
  })

  it('a sign-out never keeps the delegated context', () => {
    setActing('hub-1', 'Beta')
    clearSession()
    expect(isActing()).toBe(false)
    expect(getActingName()).toBe('')
  })

  it('switching to an instance of one\'s own drops the acting context', () => {
    setActing('hub-1', 'Beta')
    const go = vi.fn()
    switchTenant('T-A', go)
    expect(isActing()).toBe(false)
    expect(localStorage.getItem('tenantId')).toBe('T-A')
    expect(go).toHaveBeenCalledWith('/')
  })

  it('entering a Hub-only instance points the session at it and marks the context, without remembering it as an own instance', () => {
    const go = vi.fn()
    enterDelegatedInstance('T-B', 'hub-1', 'Beta', go)
    expect(localStorage.getItem('tenantId')).toBe('T-B')
    expect(getActingHub()).toBe('hub-1')
    expect(getActingName()).toBe('Beta')
    expect(localStorage.getItem('preferredTenantId')).toBeNull()
    expect(go).toHaveBeenCalledWith('/inbox?instancia=T-B')
  })

  it('leaving puts the session back on one of the person\'s OWN instances (the preferred one when it is still theirs), or none', () => {
    setActing('hub-1', 'Beta')
    localStorage.setItem('preferredTenantId', 'T-2')
    leaveDelegatedInstance([{ id: 'T-1', legal_name: 'Um' }, { id: 'T-2', legal_name: 'Dois' }])
    expect(isActing()).toBe(false)
    expect(localStorage.getItem('tenantId')).toBe('T-2')
    setActing('hub-1', 'Beta')
    localStorage.setItem('preferredTenantId', 'T-GONE')
    leaveDelegatedInstance([{ id: 'T-1', legal_name: 'Um' }])
    expect(localStorage.getItem('tenantId')).toBe('T-1')
    setActing('hub-1', 'Beta')
    leaveDelegatedInstance([])
    expect(localStorage.getItem('tenantId')).toBeNull()
  })
})
