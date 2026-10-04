import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import SettingsPage from '../pages/SettingsPage'
import { renderAt, setSession } from './testUtils'
import type { AIIntegration } from '../lib/aiIntegration'

vi.mock('axios')

const KEY = 'AIzaSyTESTKEY0123456789abcdefghijklmn'
const CONSENT = 'Ao ativar, as imagens e os PDFs escaneados serão enviados ao Google Gemini.'

function state(over: Partial<AIIntegration> = {}): AIIntegration {
  return {
    provider: 'gemini', enabled: false, model: 'gemini-2.5-flash', monthly_budget_usd: 10, key_configured: false,
    consent_text: CONSENT, consent_version: 'v1', consent_current: false, ...over,
  }
}

function server(opts: { permissions: string[]; ai?: AIIntegration }) {
  let current = opts.ai ?? state()
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: opts.permissions } }
    if (url.endsWith('/settings/inbox')) return { data: { wait_warn_minutes: 30, wait_danger_minutes: 120 } }
    if (url.endsWith('/integrations/ai')) return { data: current }
    return Promise.reject({ response: { status: 404 } })
  })
  vi.mocked(axios.put).mockImplementation(async (url: string, raw?: unknown) => {
    const body = (raw ?? {}) as Record<string, unknown>
    if (!url.endsWith('/integrations/ai')) return Promise.reject({ response: { status: 404 } })
    current = {
      ...current,
      ...(body.api_key ? { key_configured: true, key_set_at: '2026-10-04T12:00:00Z', key_set_by: 'adm@x.com' } : {}),
      ...(body.model ? { model: String(body.model) } : {}),
      ...(typeof body.monthly_budget_usd === 'number' ? { monthly_budget_usd: body.monthly_budget_usd } : {}),
      ...(typeof body.enabled === 'boolean' ? { enabled: body.enabled } : {}),
      ...(body.accept_external_ai ? { consent_current: true, consent_at: '2026-10-04T12:01:00Z', consent_by: 'adm@x.com' } : {}),
    }
    return { data: current }
  })
}

const ADMIN = ['tenant.read', 'tenant.manage']

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('AIIntegrationCard', () => {
  it('is not shown to people who cannot manage the organization, and nothing is requested', async () => {
    server({ permissions: ['tenant.read'] })
    renderAt(<SettingsPage />)
    await screen.findByText(/Tempo de espera/)
    await waitFor(() => expect(screen.queryByText(/Inteligência artificial externa/)).not.toBeInTheDocument())
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/integrations/ai'))).toBe(false)
  })

  it('starts off, with the key field as a password input that does not autofill', async () => {
    server({ permissions: ADMIN })
    renderAt(<SettingsPage />)
    expect(await screen.findByText('Desativada')).toBeInTheDocument()
    const field = screen.getByLabelText(/Chave da API do Gemini/) as HTMLInputElement
    expect(field.type).toBe('password')
    expect(field.autocomplete).toBe('new-password')
    expect(screen.getByRole('button', { name: 'Ativar integração' })).toBeDisabled()
    expect(screen.getByText(/Salve a chave para poder ativar/)).toBeInTheDocument()
  })

  it('saves the key, clears the field and never shows the key again', async () => {
    server({ permissions: ADMIN })
    renderAt(<SettingsPage />)
    const field = (await screen.findByLabelText(/Chave da API do Gemini/)) as HTMLInputElement
    await userEvent.type(field, KEY)
    await userEvent.click(screen.getByRole('button', { name: 'Salvar chave' }))
    await screen.findByText(/Chave configurada em/)
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ api_key: KEY })
    expect((screen.getByLabelText(/Substituir chave/) as HTMLInputElement).value).toBe('')
    expect(document.body.innerHTML).not.toContain('TESTKEY')
  })

  it('refuses a malformed key before sending it', async () => {
    server({ permissions: ADMIN })
    renderAt(<SettingsPage />)
    const field = await screen.findByLabelText(/Chave da API do Gemini/)
    await userEvent.type(field, 'curta')
    expect(await screen.findByText(/formato inválido/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Salvar chave' })).toBeDisabled()
    expect(vi.mocked(axios.put)).not.toHaveBeenCalled()
  })

  it('needs the key and the accepted term before it can be switched on, and sends the acceptance', async () => {
    server({ permissions: ADMIN, ai: state({ key_configured: true, key_set_at: '2026-10-04T12:00:00Z', key_set_by: 'adm@x.com' }) })
    renderAt(<SettingsPage />)
    const enable = await screen.findByRole('button', { name: 'Ativar integração' })
    expect(enable).toBeDisabled()
    expect(screen.getByText(CONSENT)).toBeInTheDocument()
    await userEvent.click(screen.getByRole('checkbox', { name: /Li e aceito/ }))
    expect(enable).toBeEnabled()
    await userEvent.click(enable)
    await screen.findByText('Ativa')
    expect(vi.mocked(axios.put).mock.calls[0][1]).toEqual({ enabled: true, accept_external_ai: true })
    expect(await screen.findByRole('button', { name: 'Desativar' })).toBeInTheDocument()
  })

  it('shows the test result with the provider-safe message', async () => {
    server({ permissions: ADMIN, ai: state({ key_configured: true }) })
    vi.mocked(axios.post).mockResolvedValue({ data: { at: '2026-10-04T12:00:00Z', ok: false, message: 'o Google recusou a chave (inválida, revogada ou sem permissão)' } })
    renderAt(<SettingsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Testar conexão' }))
    expect(await screen.findByText(/o Google recusou a chave/)).toBeInTheDocument()
  })

  it('removing the key switches the integration off', async () => {
    server({ permissions: ADMIN, ai: state({ key_configured: true, enabled: true, consent_current: true }) })
    vi.mocked(axios.delete).mockResolvedValue({ data: state({ key_configured: false, enabled: false, consent_current: true }) })
    renderAt(<SettingsPage />)
    await userEvent.click(await screen.findByRole('button', { name: 'Remover chave' }))
    expect(await screen.findByText(/Chave removida/)).toBeInTheDocument()
    expect(screen.getByText('Desativada')).toBeInTheDocument()
  })

  it('reports a refused change without claiming success', async () => {
    server({ permissions: ADMIN })
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 403 } })
    renderAt(<SettingsPage />)
    const field = await screen.findByLabelText(/Chave da API do Gemini/)
    await userEvent.type(field, KEY)
    await userEvent.click(screen.getByRole('button', { name: 'Salvar chave' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/Somente administradores/)
    expect(screen.queryByText(/Chave salva/)).not.toBeInTheDocument()
  })
})
