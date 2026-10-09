import { beforeEach, describe, expect, it, vi } from 'vitest'
import { renderHook, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import type { ReactNode } from 'react'
import TenantSwitcher from '../components/TenantSwitcher'
import { useTenantDisplay } from '../lib/tenantContext'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const A = 'aaaaaaaa-0000-0000-0000-000000000001'
const B = 'bbbbbbbb-0000-0000-0000-000000000002'
const tenants = (...t: object[]) => vi.mocked(axios.get).mockResolvedValue({ data: t })

describe('TenantSwitcher', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    setSession()
    localStorage.setItem('tenantId', A)
  })

  it('renders nothing for someone who belongs to a single tenant', async () => {
    tenants({ id: A, legal_name: 'Alfa' })
    const { container } = renderAt(<TenantSwitcher />)
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    expect(container.querySelector('select')).toBeNull()
  })

  it('renders nothing while the list is still loading or failed', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 500 } })
    const { container } = renderAt(<TenantSwitcher />)
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    expect(container.querySelector('select')).toBeNull()
  })

  it('lists the tenants by display name with the current one selected', async () => {
    tenants({ id: A, legal_name: 'Alfa Ltda', trade_name: 'Alfa Telecom' }, { id: B, legal_name: 'Beta Ltda' })
    renderAt(<TenantSwitcher />)

    const select = (await screen.findByRole('combobox', { name: 'Trocar de instância' })) as HTMLSelectElement
    expect(select.value).toBe(A)
    expect(Array.from(select.options).map((o) => o.text)).toEqual(['Alfa Telecom', 'Beta Ltda'])
  })

  it('switches tenant on selection: stores it and navigates to the start page', async () => {
    tenants({ id: A, legal_name: 'Alfa' }, { id: B, legal_name: 'Beta' })
    const go = vi.fn()
    renderAt(<TenantSwitcher go={go} />)

    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Trocar de instância' }), B)
    expect(localStorage.getItem('tenantId')).toBe(B)
    expect(localStorage.getItem('preferredTenantId')).toBe(B)
    expect(go).toHaveBeenCalledWith('/')
  })

  it('does nothing when the current tenant is picked again', async () => {
    tenants({ id: A, legal_name: 'Alfa' }, { id: B, legal_name: 'Beta' })
    const go = vi.fn()
    renderAt(<TenantSwitcher go={go} />)

    await userEvent.selectOptions(await screen.findByRole('combobox', { name: 'Trocar de instância' }), A)
    expect(go).not.toHaveBeenCalled()
  })

  // A stale tenantId (for example removed from the user) must not silently show another tenant as current.
  it('asks to pick one when the stored tenant is not in the list', async () => {
    localStorage.setItem('tenantId', 'cccccccc-0000-0000-0000-000000000003')
    tenants({ id: A, legal_name: 'Alfa' }, { id: B, legal_name: 'Beta' })
    renderAt(<TenantSwitcher />)

    const select = (await screen.findByRole('combobox', { name: 'Trocar de instância' })) as HTMLSelectElement
    expect(select.value).toBe('')
    expect(screen.getByRole('option', { name: 'Selecione…' })).toBeDisabled()
  })
})

describe('useTenantDisplay', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    localStorage.setItem('tenantId', A)
  })
  const wrapper = ({ children }: { children: ReactNode }) => (
    <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}>{children}</QueryClientProvider>
  )

  it('shows the real tenant name once the list arrives, the placeholder before', async () => {
    tenants({ id: A, legal_name: 'Alfa Ltda', trade_name: 'Alfa Telecom' })
    const { result } = renderHook(() => useTenantDisplay(), { wrapper })
    expect(result.current.tenantName).toBe('Tenant ativo')
    expect(result.current.isPlaceholderName).toBe(true)
    await waitFor(() => expect(result.current.tenantName).toBe('Alfa Telecom'))
    expect(result.current.isPlaceholderName).toBe(false)
  })

  it('keeps the placeholder when the current tenant is not in the list', async () => {
    tenants({ id: B, legal_name: 'Beta' })
    const { result } = renderHook(() => useTenantDisplay(), { wrapper })
    await waitFor(() => expect(axios.get).toHaveBeenCalled())
    expect(result.current.tenantName).toBe('Tenant ativo')
  })
})
