import { beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import { resolveSessionTenant, switchTenant, tenantDisplayName } from '../lib/tenants'

vi.mock('axios')

const A = 'aaaaaaaa-0000-0000-0000-000000000001'
const B = 'bbbbbbbb-0000-0000-0000-000000000002'

describe('tenantDisplayName', () => {
  it('prefers the trade name and falls back to the legal name', () => {
    expect(tenantDisplayName({ id: A, legal_name: 'Alfa Ltda', trade_name: 'Alfa Telecom' })).toBe('Alfa Telecom')
    expect(tenantDisplayName({ id: A, legal_name: 'Alfa Ltda' })).toBe('Alfa Ltda')
    expect(tenantDisplayName({ id: A, legal_name: 'Alfa Ltda', trade_name: '   ' })).toBe('Alfa Ltda')
  })
})

describe('resolveSessionTenant', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
  })

  it('keeps the server default when the user never chose a tenant, without calling the API', async () => {
    await expect(resolveSessionTenant(A)).resolves.toBe(A)
    expect(axios.get).not.toHaveBeenCalled()
  })

  it('gives back the tenant the user last chose when it is still one of theirs', async () => {
    localStorage.setItem('preferredTenantId', B)
    vi.mocked(axios.get).mockResolvedValue({ data: [{ id: A, legal_name: 'A' }, { id: B, legal_name: 'B' }] })
    await expect(resolveSessionTenant(A)).resolves.toBe(B)
  })

  // The id comes from the browser: it is only honoured if the server lists it for this user.
  it('ignores a remembered tenant that is not in the user\'s list', async () => {
    localStorage.setItem('preferredTenantId', B)
    vi.mocked(axios.get).mockResolvedValue({ data: [{ id: A, legal_name: 'A' }] })
    await expect(resolveSessionTenant(A)).resolves.toBe(A)
  })

  it('falls back to the server default when the list cannot be loaded', async () => {
    localStorage.setItem('preferredTenantId', B)
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 500 } })
    await expect(resolveSessionTenant(A)).resolves.toBe(A)
  })

  it('does not ask the API when the remembered tenant already is the default', async () => {
    localStorage.setItem('preferredTenantId', A)
    await expect(resolveSessionTenant(A)).resolves.toBe(A)
    expect(axios.get).not.toHaveBeenCalled()
  })

  it('still returns undefined when there is no default and nothing valid is remembered', async () => {
    await expect(resolveSessionTenant(undefined)).resolves.toBeUndefined()
  })
})

describe('switchTenant', () => {
  beforeEach(() => localStorage.clear())

  it('stores the tenant as current and as preferred, then does a full navigation to the start page', () => {
    const go = vi.fn()
    switchTenant(B, go)
    expect(localStorage.getItem('tenantId')).toBe(B)
    expect(localStorage.getItem('preferredTenantId')).toBe(B)
    expect(go).toHaveBeenCalledWith('/')
  })
})
