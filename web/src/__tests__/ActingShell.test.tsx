import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import { MemoryRouter, Route, Routes } from 'react-router-dom'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import Sidebar from '../components/Sidebar'
import MobileNav from '../components/MobileNav'
import Layout from '../components/Layout'
import DelegatedContextPane from '../components/inbox/DelegatedContextPane'
import MessageMedia from '../components/inbox/MessageMedia'
import { setActing, clearActing } from '../lib/acting'
import { setSession } from './testUtils'

// not a module mock: the shell imports the app's own axios instance (lib/api) at load, which an automock would break
vi.mock('../hooks/usePresenceHeartbeat', () => ({ usePresenceHeartbeat: () => {} }))
const get = vi.spyOn(axios, 'get')

function wrap(ui: React.ReactElement, path = '/inbox') {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter initialEntries={[path]}>
        <Routes>
          <Route path="/inbox" element={ui} />
          <Route path="*" element={<div data-testid="elsewhere">elsewhere</div>} />
        </Routes>
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

beforeEach(() => {
  localStorage.clear()
  setSession()
  get.mockReset()
  get.mockImplementation(async (url: string) => {
    if (String(url).endsWith('/hubs')) return { data: { items: [{ id: 'hub-1', name: 'K3G', role: 'hub_admin', can_manage_access: true, can_manage_instances: true }] } }
    if (String(url).endsWith('/tenants')) return { data: [{ id: 'tenant-a-uuid', legal_name: 'Alfa' }] }
    return Promise.reject({ response: { status: 404 } })
  })
})
afterEach(() => clearActing())

describe('the shell while attending an instance through the Hub', () => {
  it('shows every menu entry as usual when not acting', async () => {
    wrap(<Sidebar />)
    expect(await screen.findByRole('link', { name: 'Conversas' })).toBeInTheDocument()
    expect(screen.getByRole('link', { name: 'Tickets' })).toBeInTheDocument()
    expect(await screen.findByRole('link', { name: 'Acessos' })).toBeInTheDocument()
  })

  it('offers only the conversations (desktop and mobile), and names the instance, while acting', async () => {
    setActing('hub-1', 'Beta Hub')
    wrap(
      <>
        <Sidebar />
        <MobileNav />
      </>,
    )
    expect((await screen.findAllByRole('link', { name: 'Conversas' })).length).toBeGreaterThan(0)
    for (const hidden of ['Tickets', 'Contatos', 'Canais', 'Grupos', 'Automação', 'Supervisor', 'Pessoas', 'Configurações', 'Acessos', 'Canais das instâncias']) {
      expect(screen.queryByRole('link', { name: hidden })).toBeNull()
    }
    expect(screen.getAllByText('Beta Hub').length).toBeGreaterThan(0)
    expect(screen.getAllByText('Atendendo pelo Hub').length).toBeGreaterThan(0)
  })

  it('any other address goes back to the conversations while acting', async () => {
    setActing('hub-1', 'Beta Hub')
    render(
      <QueryClientProvider client={new QueryClient()}>
        <MemoryRouter initialEntries={['/tickets']}>
          <Routes>
            <Route path="/" element={<Layout />}>
              <Route path="tickets" element={<div data-testid="tickets">tickets</div>} />
              <Route path="inbox" element={<div data-testid="inbox">inbox</div>} />
            </Route>
          </Routes>
        </MemoryRouter>
      </QueryClientProvider>,
    )
    await waitFor(() => expect(screen.getByTestId('inbox')).toBeInTheDocument())
    expect(screen.queryByTestId('tickets')).toBeNull()
  })
})

describe('the delegated details card', () => {
  it('shows who the customer is and where the attendance stands, read-only, and says what comes next', async () => {
    setActing('hub-1', 'Beta Hub')
    get.mockImplementation(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', contact_phone: '+559284484206', status: 'open', contact_id: 'k1', assigned_to_user_id: 'u9', assigned_to_name: 'Maria', message_count: 9 } }
      if (u.endsWith('/contacts/k1')) return { data: { id: 'k1', display_name: 'Berjon Brito', whatsapp_name: 'Berjon B', phone_e164: '+559284484206', kind: 'customer' } }
      return Promise.reject({ response: { status: 404 } })
    })
    wrap(<DelegatedContextPane conversationId="c1" />)
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(await screen.findByText('Cliente')).toBeInTheDocument()
    expect(screen.getByText('Maria')).toBeInTheDocument()
    expect(screen.getByText('9')).toBeInTheDocument()
    expect(screen.getByText(/Atendendo Beta Hub pelo Hub/)).toBeInTheDocument()
    expect(screen.getByRole('note')).toHaveTextContent('chegam na próxima etapa')
    // read-only: no edit, no classify, no ticket buttons
    expect(screen.queryAllByRole('button')).toHaveLength(0)
  })

  it('without contact.read the card says so instead of failing', async () => {
    setActing('hub-1', 'Beta Hub')
    get.mockImplementation(async (url: string) => {
      const u = String(url)
      if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', contact_phone: '+559284484206', status: 'open', contact_id: 'k1' } }
      return Promise.reject({ response: { status: 403 } })
    })
    wrap(<DelegatedContextPane conversationId="c1" />)
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(await screen.findByText(/não estão liberados para o seu acesso/)).toBeInTheDocument()
  })
})

describe('message files while acting', () => {
  const message = { id: 'M1', direction: 'inbound', message_type: 'image', body: '', mime_type: 'image/png', media_status: 'clean', created_at: new Date().toISOString() } as never
  it('load from the tenant route normally', () => {
    wrap(<MessageMedia message={message} tenantId="T-B" />)
    expect(screen.getByRole('img')).toHaveAttribute('src', '/api/v1/tenants/T-B/messages/M1/media')
  })
  it('load from the hub path while acting (an <img> cannot send the acting header)', () => {
    setActing('hub-1', 'Beta Hub')
    wrap(<MessageMedia message={message} tenantId="T-B" />)
    expect(screen.getByRole('img')).toHaveAttribute('src', '/api/v1/hubs/hub-1/serve/T-B/messages/M1/media')
  })
})
