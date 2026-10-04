import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import SettingsPage, { formatMinutes } from '../pages/SettingsPage'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

function server(opts: { permissions: string[]; settings?: { wait_warn_minutes: number; wait_danger_minutes: number }; readFails?: boolean }) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: opts.permissions } }
    if (url.endsWith('/settings/inbox')) {
      if (opts.readFails) return Promise.reject({ response: { status: 500 } })
      return { data: opts.settings ?? { wait_warn_minutes: 30, wait_danger_minutes: 120 } }
    }
    if (url.endsWith('/integrations/ai')) {
      return { data: { provider: 'gemini', enabled: false, model: 'gemini-2.5-flash', monthly_budget_usd: 10, key_configured: false, consent_text: 't', consent_version: 'v1', consent_current: false } }
    }
    return Promise.reject({ response: { status: 404 } })
  })
}

// The form is editable only after the permissions load; wait for that, not just for the field to exist.
async function editableForm() {
  const warn = (await screen.findByLabelText(/Atenção/)) as HTMLInputElement
  await waitFor(() => expect(warn).toBeEnabled())
  return { warn, danger: screen.getByLabelText(/Crítico/) as HTMLInputElement }
}

const ADMIN = ['tenant.read', 'tenant.manage']
const AGENT = ['tenant.read']

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('formatMinutes', () => {
  it('uses min, h and d like the list', () => {
    expect(formatMinutes(30)).toBe('30 min')
    expect(formatMinutes(120)).toBe('2 h')
    expect(formatMinutes(90)).toBe('90 min')
    expect(formatMinutes(1440)).toBe('1 d')
    expect(formatMinutes(10080)).toBe('7 d')
  })
})

describe('SettingsPage — wait thresholds', () => {
  it('does not claim "only administrators" while the permissions are still loading', async () => {
    let release: (v: unknown) => void = () => {}
    vi.mocked(axios.get).mockImplementation((url: string) => {
      if (url.endsWith('/me/access')) return new Promise((resolve) => { release = () => resolve({ data: { role_key: 'x', permissions: ADMIN } }) })
      return Promise.resolve({ data: { wait_warn_minutes: 30, wait_danger_minutes: 120 } })
    })
    renderAt(<SettingsPage />)
    await screen.findByLabelText(/Atenção/)
    expect(screen.queryByText(/Somente administradores/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Salvar' })).not.toBeInTheDocument()
    release(undefined)
    expect(await screen.findByRole('button', { name: 'Salvar' })).toBeInTheDocument()
  })

  it('shows the saved values and a live preview of the three bands', async () => {
    server({ permissions: ADMIN, settings: { wait_warn_minutes: 45, wait_danger_minutes: 180 } })
    renderAt(<SettingsPage />)
    expect(await screen.findByDisplayValue('45')).toBeInTheDocument()
    expect(screen.getByDisplayValue('180')).toBeInTheDocument()
    expect(screen.getByText('menos de 45 min')).toBeInTheDocument()
    expect(screen.getByText('45 min a 3 h')).toBeInTheDocument()
    expect(screen.getByText('a partir de 3 h')).toBeInTheDocument()
  })

  it('lets an admin change and save, sending exactly the two numbers', async () => {
    const user = userEvent.setup()
    server({ permissions: ADMIN })
    vi.mocked(axios.put).mockResolvedValue({ data: { wait_warn_minutes: 15, wait_danger_minutes: 60 } })
    renderAt(<SettingsPage />)
    const { warn, danger } = await editableForm()
    const save = await screen.findByRole('button', { name: 'Salvar' })
    expect(save).toBeDisabled() // nothing changed yet
    await user.clear(warn)
    await user.type(warn, '15')
    await user.clear(danger)
    await user.type(danger, '60')
    expect(save).toBeEnabled()
    await user.click(save)
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ wait_warn_minutes: 15, wait_danger_minutes: 60 })
    expect(await screen.findByText('Salvo.')).toBeInTheDocument()
  })

  it('lets you clear a field to type a new number without it snapping back', async () => {
    const user = userEvent.setup()
    server({ permissions: ADMIN })
    renderAt(<SettingsPage />)
    const { warn } = await editableForm()
    await user.clear(warn)
    expect(warn.value).toBe('')
    await user.type(warn, '7')
    expect(warn.value).toBe('7')
  })

  it('blocks invalid values with a reason and never calls the API', async () => {
    const user = userEvent.setup()
    server({ permissions: ADMIN })
    renderAt(<SettingsPage />)
    const { warn, danger } = await editableForm()
    await user.clear(warn)
    await user.type(warn, '200') // attention above critical (120)
    expect(await screen.findByRole('alert')).toHaveTextContent('maior')
    expect(screen.getByRole('button', { name: 'Salvar' })).toBeDisabled()
    await user.clear(danger)
    await user.type(danger, '12a')
    expect(screen.getByRole('alert')).toHaveTextContent('inteiros')
    await user.clear(warn)
    await user.type(warn, '0')
    await user.clear(danger)
    await user.type(danger, '5')
    expect(screen.getByRole('alert')).toHaveTextContent('inteiros')
    expect(axios.put).not.toHaveBeenCalled()
  })

  it('restores the standard 30 min / 2 h and only offers it when different', async () => {
    const user = userEvent.setup()
    server({ permissions: ADMIN, settings: { wait_warn_minutes: 10, wait_danger_minutes: 20 } })
    renderAt(<SettingsPage />)
    const restore = await screen.findByRole('button', { name: /Restaurar padrão/ })
    expect(restore).toBeEnabled()
    await user.click(restore)
    expect(screen.getByLabelText(/Atenção/)).toHaveValue('30')
    expect(screen.getByLabelText(/Crítico/)).toHaveValue('120')
    expect(restore).toBeDisabled()
  })

  it('shows the API reason when saving is refused, and keeps what was typed', async () => {
    const user = userEvent.setup()
    server({ permissions: ADMIN })
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 403 } })
    renderAt(<SettingsPage />)
    const { warn } = await editableForm()
    await user.clear(warn)
    await user.type(warn, '20')
    await user.click(await screen.findByRole('button', { name: 'Salvar' }))
    expect(await screen.findByText(/Somente administradores podem alterar estes limites\./)).toBeInTheDocument()
    expect(screen.getByLabelText(/Atenção/)).toHaveValue('20')
    expect(screen.queryByText('Salvo.')).not.toBeInTheDocument()
  })

  it('is read-only without tenant.manage: values visible, no way to save', async () => {
    server({ permissions: AGENT, settings: { wait_warn_minutes: 45, wait_danger_minutes: 180 } })
    renderAt(<SettingsPage />)
    expect(await screen.findByDisplayValue('45')).toBeDisabled()
    expect(screen.getByDisplayValue('180')).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Salvar' })).not.toBeInTheDocument()
    expect(screen.getByText('Somente administradores podem alterar estes limites.')).toBeInTheDocument()
  })

  it('does not offer the form when the settings cannot be read', async () => {
    server({ permissions: ADMIN, readFails: true })
    renderAt(<SettingsPage />)
    expect(await screen.findByRole('alert')).toHaveTextContent('Não foi possível carregar')
    expect(screen.queryByRole('button', { name: 'Salvar' })).not.toBeInTheDocument()
  })

  it('refuses the page to someone without tenant.read', async () => {
    server({ permissions: [] })
    renderAt(<SettingsPage />)
    expect(await screen.findByText(/não tem permissão/i)).toBeInTheDocument()
  })
})
