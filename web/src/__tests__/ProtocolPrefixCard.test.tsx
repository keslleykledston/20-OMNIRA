import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import SettingsPage from '../pages/SettingsPage'
import { renderAt, setSession } from './testUtils'
import type { ProtocolPrefix } from '../lib/serviceContext'

vi.mock('axios')

function server(opts: { permissions: string[]; prefix?: ProtocolPrefix | 'absent'; put?: (body: { prefix: string }) => ProtocolPrefix | { status: number; error: string } }) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: opts.permissions } }
    if (url.endsWith('/settings/inbox')) return { data: { wait_warn_minutes: 30, wait_danger_minutes: 120 } }
    if (url.endsWith('/integrations/ai')) return Promise.reject({ response: { status: 404 } })
    if (url.endsWith('/service-settings/protocol-prefix')) {
      if (opts.prefix === 'absent') return Promise.reject({ response: { status: 404 } })
      return { data: opts.prefix ?? { prefix: 'KTG', configured: false, locked: false, next_protocol: 'KTG-1001' } }
    }
    return Promise.reject({ response: { status: 404 } })
  })
  vi.mocked(axios.put).mockImplementation(async (url: string, raw?: unknown) => {
    if (!url.endsWith('/service-settings/protocol-prefix')) return Promise.reject({ response: { status: 404 } })
    const out = opts.put?.(raw as { prefix: string })
    if (out && 'status' in out) return Promise.reject({ response: { status: out.status, data: { error: out.error } } })
    const prefix = (raw as { prefix: string }).prefix
    return { data: out ?? { prefix, configured: true, locked: false, next_protocol: `${prefix}-1001` } }
  })
}

describe('ProtocolPrefixCard (Configurações)', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    setSession()
  })

  it('an administrator sees the suggestion and reserves another prefix, sent in capitals', async () => {
    server({ permissions: ['tenant.read', 'tenant.manage'] })
    renderAt(<SettingsPage />)
    const input = (await screen.findByLabelText('Prefixo')) as HTMLInputElement
    expect(input.value).toBe('KTG')
    await userEvent.clear(input)
    await userEvent.type(input, 'k3x9z')
    expect(input.value).toBe('KXZ')
    await userEvent.click(screen.getByRole('button', { name: 'Reservar prefixo' }))
    await waitFor(() => expect(vi.mocked(axios.put)).toHaveBeenCalled())
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ prefix: 'KXZ' })
    expect(await screen.findByText(/Prefixo KXZ reservado/)).toBeInTheDocument()
  })

  it('one letter is not enough: the button stays off and nothing is sent', async () => {
    server({ permissions: ['tenant.read', 'tenant.manage'] })
    renderAt(<SettingsPage />)
    const input = await screen.findByLabelText('Prefixo')
    await userEvent.clear(input)
    await userEvent.type(input, 'K')
    expect(screen.getByRole('button', { name: 'Reservar prefixo' })).toBeDisabled()
    expect(vi.mocked(axios.put)).not.toHaveBeenCalled()
  })

  it('a prefix of another company says so', async () => {
    server({ permissions: ['tenant.read', 'tenant.manage'], put: () => ({ status: 409, error: 'prefix_taken' }) })
    renderAt(<SettingsPage />)
    const input = await screen.findByLabelText('Prefixo')
    await userEvent.clear(input)
    await userEvent.type(input, 'abc')
    await userEvent.click(screen.getByRole('button', { name: 'Reservar prefixo' }))
    expect(await screen.findByText('Outra empresa já usa este prefixo. Escolha outro.')).toBeInTheDocument()
  })

  it('after the first protocol the prefix is locked: no field to edit, no button', async () => {
    server({ permissions: ['tenant.read', 'tenant.manage'], prefix: { prefix: 'KTG', configured: true, locked: true, next_protocol: 'KTG-1006' } })
    renderAt(<SettingsPage />)
    const input = (await screen.findByLabelText('Prefixo')) as HTMLInputElement
    expect(input).toBeDisabled()
    expect(input.value).toBe('KTG')
    expect(screen.queryByRole('button', { name: 'Reservar prefixo' })).not.toBeInTheDocument()
  })

  it('a member without tenant.manage does not see it, and the endpoint is not even asked', async () => {
    server({ permissions: ['tenant.read'] })
    renderAt(<SettingsPage />)
    await screen.findByText('Tempo de espera na lista de conversas')
    expect(screen.queryByText('Prefixo do protocolo de atendimento')).not.toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/service-settings/protocol-prefix'))).toBe(false)
  })

  it('hidden when the service module is off on the platform', async () => {
    server({ permissions: ['tenant.read', 'tenant.manage'], prefix: 'absent' })
    renderAt(<SettingsPage />)
    await screen.findByText('Tempo de espera na lista de conversas')
    await waitFor(() => expect(screen.queryByText('Prefixo do protocolo de atendimento')).not.toBeInTheDocument())
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/service-settings/protocol-prefix'))).toBe(true)
  })
})
