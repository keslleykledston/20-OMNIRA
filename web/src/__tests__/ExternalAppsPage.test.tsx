import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ExternalAppsPage from '../pages/ExternalAppsPage'
import { renderAt, setSession } from './testUtils'
import { validateWebhookURL, type ExternalApp } from '../lib/externalApps'

vi.mock('axios')

const ADMIN = ['integration.read', 'integration.manage']
const TOKEN = 'omn_ext_abc123.SECRETSECRETSECRET'

function app(over: Partial<ExternalApp> = {}): ExternalApp {
  return {
    id: 'app-1', name: 'CRM Acme', status: 'active', scopes: ['conversation.read', 'events.read'], queue_ids: [], channel_ids: [], rate_limit_per_min: 600,
    lease_seconds: null, allow_outside_hours: false, created_at: '2026-10-14T12:00:00Z', updated_at: '2026-10-14T12:00:00Z', suspended_at: null, revoked_at: null,
    credentials: [{ id: 'c1', key_id: 'k1', label: 'primeiro', created_at: '2026-10-14T12:00:00Z', expires_at: '2027-01-14T12:00:00Z', revoked_at: null, last_used_at: null, live: true }], ...over,
  }
}

function server(opts: { permissions: string[]; apps?: ExternalApp[]; listStatus?: number }) {
  const apps = opts.apps ?? []
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: opts.permissions } }
    if (url.endsWith('/integrations/apps')) {
      if (opts.listStatus) return Promise.reject({ response: { status: opts.listStatus } })
      return { data: { items: apps, count: apps.length } }
    }
    if (/\/integrations\/apps\/app-1$/.test(url)) return { data: apps[0] }
    if (url.endsWith('/webhooks')) return { data: { items: [], count: 0 } }
    if (url.endsWith('/queues')) return { data: { items: [{ id: 'q1', name: 'Suporte', mode: 'manual', is_default: true }] } }
    if (url.endsWith('/channels/connections') || url.endsWith('/connections')) return { data: { items: [] } }
    return Promise.reject({ response: { status: 404 } })
  })
  vi.mocked(axios.post).mockImplementation(async (url: string, body?: unknown) => {
    if (url.endsWith('/integrations/apps')) {
      return { data: { installation: app({ id: 'app-9', name: String((body as { name: string }).name) }), credential: { id: 'c9', key_id: 'k9', label: '', created_at: '', expires_at: '', revoked_at: null, last_used_at: null, live: true, token: TOKEN } } }
    }
    return Promise.reject({ response: { status: 404 } })
  })
}

describe('ExternalAppsPage', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    setSession()
  })

  it('lists the installed applications for someone who can read, without offering to create', async () => {
    server({ permissions: ['integration.read'], apps: [app()] })
    renderAt(<ExternalAppsPage />)
    expect(await screen.findByLabelText('Aplicação CRM Acme')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Nova aplicação/ })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Ver' })).toBeInTheDocument()
  })

  it('refuses someone without permission, and says when the feature is not enabled here', async () => {
    server({ permissions: [] })
    renderAt(<ExternalAppsPage />)
    expect(await screen.findByText(/não tem permissão para ver as aplicações externas/)).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/integrations/apps'))).toBe(false)
  })

  it('explains a 404 as "not enabled in this environment", not as an error', async () => {
    server({ permissions: ADMIN, listStatus: 404 })
    renderAt(<ExternalAppsPage />)
    expect(await screen.findByText(/Recurso não habilitado neste ambiente/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Nova aplicação/ })).not.toBeInTheDocument()
  })

  it('creates an application and shows the token exactly once', async () => {
    server({ permissions: ADMIN })
    renderAt(<ExternalAppsPage />)
    await userEvent.click(await screen.findByRole('button', { name: '+ Nova aplicação' }))
    await userEvent.type(await screen.findByLabelText('Nome da aplicação'), 'CRM Novo')
    await userEvent.click(screen.getByRole('button', { name: 'Criar aplicação' }))
    const secret = await screen.findByTestId('secret-once')
    expect(secret.textContent).toBe(TOKEN)
    const sent = vi.mocked(axios.post).mock.calls[0][1] as Record<string, unknown>
    expect(sent.name).toBe('CRM Novo')
    expect(sent.scopes).toEqual(['conversation.read', 'message.read', 'contact.read', 'events.read'])
    expect(sent.queue_ids).toEqual([])
    // closing the panel removes the token from the page for good
    await userEvent.click(screen.getByRole('button', { name: 'Já guardei, fechar' }))
    await waitFor(() => expect(screen.queryByTestId('secret-once')).not.toBeInTheDocument())
    expect(document.body.innerHTML).not.toContain('SECRETSECRET')
  })

  it('warns when write permissions are chosen and blocks an empty name or no permission', async () => {
    server({ permissions: ADMIN })
    renderAt(<ExternalAppsPage />)
    await userEvent.click(await screen.findByRole('button', { name: '+ Nova aplicação' }))
    const create = await screen.findByRole('button', { name: 'Criar aplicação' })
    expect(create).toBeDisabled()
    await userEvent.type(screen.getByLabelText('Nome da aplicação'), 'X')
    expect(create).toBeEnabled()
    expect(screen.queryByText(/responde clientes e assume conversas/)).not.toBeInTheDocument()
    await userEvent.click(screen.getByLabelText(/Enviar mensagens ao cliente/))
    expect(screen.getByText(/responde clientes e assume conversas/)).toBeInTheDocument()
  })

  it('validates a webhook address the way the server will', () => {
    expect(validateWebhookURL('https://crm.exemplo.com/hook')).toBeNull()
    expect(validateWebhookURL('https://crm.exemplo.com:8443/hook')).toBeNull()
    expect(validateWebhookURL('http://crm.exemplo.com/hook')).toMatch(/https/)
    expect(validateWebhookURL('https://crm.exemplo.com:8080/hook')).toMatch(/porta/)
    expect(validateWebhookURL('https://u:p@crm.exemplo.com/hook')).toMatch(/usuário/)
    expect(validateWebhookURL('crm.exemplo.com')).toMatch(/completo/)
  })
})
