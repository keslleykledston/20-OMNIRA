import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import axios from 'axios'
import { AdminRoutes } from '../admin/AdminApp'
import ToConsole from '../components/ToConsole'
import Sidebar from '../components/Sidebar'
import MobileNav from '../components/MobileNav'
import { isConsolePath, rememberAfterLogin, takeAfterLogin } from '../lib/afterLogin'
import { clearActing, setActing } from '../lib/acting'
import { setSession } from './testUtils'

// ADR-0041: the administration console is its own entry of the build, served under /admin/. These tests pin what the person sees and where each address goes;
// the server still decides every request.
vi.mock('../hooks/usePresenceHeartbeat', () => ({ usePresenceHeartbeat: vi.fn() }))
const get = vi.spyOn(axios, 'get')
const replace = vi.fn()
const realLocation = window.location

let hubs: object[] = []
// undefined: this session's user is not a platform operator (the server answers 404, same as any
// other route this mock does not know about — no enumeration oracle, mirrors updatemgr's own 404).
let installation: object | 'transient-error' | undefined

function client() {
  return new QueryClient({ defaultOptions: { queries: { retry: false } } })
}
function inConsole(path: string) {
  return render(
    <QueryClientProvider client={client()}>
      <MemoryRouter initialEntries={[path]}>
        <AdminRoutes />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  sessionStorage.clear()
  clearActing()
  replace.mockReset()
  Object.defineProperty(window, 'location', { configurable: true, value: { ...realLocation, pathname: '/admin/acessos', search: '', replace, assign: vi.fn() } })
  hubs = [{ id: 'hub-1', name: 'K3G', role: 'hub_admin', can_manage_access: true, can_manage_instances: true }]
  installation = undefined
  get.mockReset()
  get.mockImplementation(async (url: string) => {
    if (String(url).endsWith('/hubs')) return { data: { items: hubs } }
    if (String(url).endsWith('/platform/installation')) {
      if (installation === 'transient-error') return Promise.reject({ response: { status: 500 } })
      if (installation) return { data: installation }
      return Promise.reject({ response: { status: 404 } })
    }
    return Promise.reject({ response: { status: 404 } })
  })
})
afterEach(() => {
  Object.defineProperty(window, 'location', { configurable: true, value: realLocation })
  clearActing()
})

describe('console shell', () => {
  it('shows the navigation and the way back to the attendance to a Hub administrator', async () => {
    setSession()
    inConsole('/acessos')
    expect(await screen.findByRole('link', { name: 'Acessos' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Canais das instâncias' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Voltar ao atendimento' })).toHaveAttribute('href', '/inbox')
    expect(screen.getByText('Operador Teste')).toBeInTheDocument()
  })

  it('offers only what the server allowed: channel management alone does not show Acessos', async () => {
    setSession()
    hubs = [{ id: 'hub-1', name: 'K3G', role: 'hub_agent', can_manage_instances: true }]
    inConsole('/instancias')
    expect(await screen.findByRole('link', { name: 'Canais das instâncias' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Acessos' })).toBeNull()
  })

  it('says there is no access to someone with no Hub administration, and renders no console page', async () => {
    setSession()
    hubs = [{ id: 'hub-1', name: 'K3G', role: 'hub_agent' }]
    inConsole('/acessos')
    expect(await screen.findByText('Sem acesso ao console')).toBeInTheDocument()
    expect(screen.queryByRole('tablist')).toBeNull()
  })

  it('a platform operator with no Hub grant at all sees the console (Plataforma only)', async () => {
    setSession()
    hubs = []
    installation = { channel: 'stable', agent_protocol: 1, current_digests: {}, updated_at: new Date().toISOString() }
    inConsole('/plataforma')
    expect(await screen.findByRole('link', { name: 'Plataforma' })).toBeInTheDocument()
    expect(screen.queryByRole('link', { name: 'Acessos' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Canais das instâncias' })).toBeNull()
    expect(screen.queryByText('Sem acesso ao console')).toBeNull()
  })

  it('a transient probe failure (not a confirmed 404) never says "Sem acesso" to a platform-only operator — offers retry instead (Codex round 13)', async () => {
    setSession()
    hubs = []
    installation = 'transient-error'
    inConsole('/plataforma')
    expect(await screen.findByText('Não foi possível confirmar seu acesso')).toBeInTheDocument()
    expect(screen.queryByText('Sem acesso ao console')).toBeNull()
    expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument()
  })

  it('someone who is only a Hub admin (never a platform operator) never sees Plataforma', async () => {
    setSession()
    installation = undefined
    inConsole('/acessos')
    await screen.findByRole('link', { name: 'Acessos' })
    expect(screen.queryByRole('link', { name: 'Plataforma' })).toBeNull()
  })

  it('a platform operator who is also a Hub admin sees all three tabs', async () => {
    setSession()
    installation = { channel: 'stable', agent_protocol: 1, current_digests: {}, updated_at: new Date().toISOString() }
    inConsole('/acessos')
    expect(await screen.findByRole('link', { name: 'Acessos' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Canais das instâncias' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Plataforma' })).toBeInTheDocument()
  })

  it('without a session remembers the console address and sends the person to sign in', async () => {
    Object.defineProperty(window, 'location', { configurable: true, value: { ...realLocation, pathname: '/admin/instancias', search: '?x=1', replace, assign: vi.fn() } })
    inConsole('/instancias')
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/login'))
    expect(takeAfterLogin()).toBe('/admin/instancias?x=1')
  })

  it('the front door sends a platform-operator-only person straight to Plataforma, never Acessos (which would just say so is unavailable)', async () => {
    setSession()
    hubs = []
    installation = { channel: 'stable', agent_protocol: 1, current_digests: {}, updated_at: new Date().toISOString() }
    inConsole('/')
    expect(await screen.findByRole('link', { name: 'Plataforma' })).toBeInTheDocument()
    expect(screen.getByText('Versões e atualizações')).toBeInTheDocument()
  })

  it('while attending an instance through the Hub the console is not offered: back to the conversations', async () => {
    setSession()
    setActing('hub-1', 'Beta')
    inConsole('/acessos')
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/inbox'))
    expect(screen.queryByRole('link', { name: 'Acessos' })).toBeNull()
  })
})

describe('return after sign-in', () => {
  it('accepts only console addresses, once', () => {
    expect(isConsolePath('/admin/')).toBe(true)
    expect(isConsolePath('/admin')).toBe(true)
    expect(isConsolePath('/admin/acessos?aba=equipes')).toBe(true)
    for (const bad of ['/inbox', 'https://evil.example/admin/', '//evil.example', '/administrator', '/admin\\evil', '']) expect(isConsolePath(bad), bad).toBe(false)
    rememberAfterLogin('/inbox')
    expect(takeAfterLogin()).toBeNull()
    rememberAfterLogin('/admin/acessos')
    expect(takeAfterLogin()).toBe('/admin/acessos')
    expect(takeAfterLogin()).toBeNull()
  })

  it('never trusts storage that was tampered with', () => {
    sessionStorage.setItem('omnira.afterLogin', 'https://evil.example/')
    expect(takeAfterLogin()).toBeNull()
  })
})

describe('the agent app', () => {
  it('old administration addresses redirect to the console (full navigation)', async () => {
    Object.defineProperty(window, 'location', { configurable: true, value: { ...realLocation, replace, assign: vi.fn() } })
    const { unmount } = render(
      <MemoryRouter initialEntries={['/instancias/h/t/canais?x=1']}>
        <ToConsole />
      </MemoryRouter>,
    )
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/admin/instancias/h/t/canais?x=1'))
    unmount()
    replace.mockReset()
    render(
      <MemoryRouter initialEntries={['/hub/empresas']}>
        <ToConsole to="/acessos?aba=instancias" />
      </MemoryRouter>,
    )
    await waitFor(() => expect(replace).toHaveBeenCalledWith('/admin/acessos?aba=instancias'))
  })

  it('the menus carry a single link to the console, only for people the server lets administer', async () => {
    setSession()
    const view = render(
      <QueryClientProvider client={client()}>
        <MemoryRouter initialEntries={['/inbox']}>
          <Sidebar />
          <MobileNav />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    const links = await screen.findAllByRole('link', { name: 'Administração' })
    expect(links).toHaveLength(2)
    for (const l of links) expect(l).toHaveAttribute('href', '/admin/')
    expect(screen.queryByRole('link', { name: 'Acessos' })).toBeNull()
    expect(screen.queryByRole('link', { name: 'Canais das instâncias' })).toBeNull()
    view.unmount()

    hubs = [{ id: 'hub-1', name: 'K3G', role: 'hub_agent' }]
    render(
      <QueryClientProvider client={client()}>
        <MemoryRouter initialEntries={['/inbox']}>
          <Sidebar />
          <MobileNav />
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await screen.findAllByRole('link', { name: 'Conversas' })
    await waitFor(() => expect(get).toHaveBeenCalled())
    expect(screen.queryByRole('link', { name: 'Administração' })).toBeNull()
  })
})
