import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import HubContextPane from '../components/hub/HubContextPane'
import { render } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { MemoryRouter } from 'react-router-dom'
import { renderAt, setSession } from './testUtils'
import type { HubItemDetail } from '../lib/hub'

vi.mock('axios')

const HUB = 'hub-1'
const COMPANY = 'company-k3g'
const CONV = 'conv-1'

const detail = (serviceKeys?: string[], company = COMPANY, conv = CONV): HubItemDetail => ({
  item: { id: 'item-1', tenant_id: company, tenant_name: 'K3G_Solutions', conversation_id: conv, customer_name: 'Matheus Oliveira', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 } as any,
  tenant: { id: company, name: 'K3G_Solutions' },
  access: { source: 'hub', hub_id: HUB, can_reply: true, ...(serviceKeys ? { keys: serviceKeys, service_keys: serviceKeys.filter((k) => k.startsWith('service.')) } : {}) },
  conversation: { id: conv, status: 'open', created_at: '2026-10-15T12:00:00Z', assignment: 'me' },
  messages: [],
})

const session = { id: 's-1', protocol: 'KTG-1005', conversation_id: CONV, status: 'open', opened_at: '2026-10-15T12:00:00Z', version: 1, origin_channel: 'whatsapp', reopen_count: 0 }

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/conversations/${CONV}/service-session`)) return { data: session }
    if (url.endsWith('/service-tasks')) return { data: { items: [] } }
    return Promise.reject({ response: { status: 404 } })
  })
})

describe('HubContextPane — protocol and tasks through the Hub (ADR-0049)', () => {
  it('shows the protocol of the conversation, asking the COMPANY of the item with the Hub in the acting header', async () => {
    renderAt(<HubContextPane detail={detail(['service.read', 'service.create'])} fullContext={false} onOpenFull={() => {}} />)
    expect(await screen.findByTestId('service-protocol')).toHaveTextContent('KTG-1005')
    const [url, config] = vi.mocked(axios.get).mock.calls.find(([u]) => String(u).endsWith('/service-session'))!
    expect(String(url)).toContain(`/tenants/${COMPANY}/conversations/${CONV}/service-session`)
    expect((config as any).headers['X-Omnira-Acting-As']).toBe(`hub:${HUB}`)
  })

  it('offers no protocol section (and asks nothing) when the grant holds no service key', async () => {
    renderAt(<HubContextPane detail={detail([])} fullContext={false} onOpenFull={() => {}} />)
    await screen.findByText('Linha do tempo')
    expect(screen.queryByText('Protocolo e tarefas')).not.toBeInTheDocument()
    await waitFor(() => expect(vi.mocked(axios.get)).not.toHaveBeenCalled())
  })

  it('an old server that does not send the keys shows nothing either', async () => {
    renderAt(<HubContextPane detail={detail()} fullContext={false} onOpenFull={() => {}} />)
    await screen.findByText('Linha do tempo')
    expect(screen.queryByText('Protocolo e tarefas')).not.toBeInTheDocument()
  })

  // ---- slice 1 of the parity with the full workspace: contact, classification and the ERP ticket, for the item's company
  const FULL = ['conversation.read', 'contact.read', 'contact.classify', 'ticket.read', 'ticket.create']
  const conversationOf = (kind: string, name: string) => ({ id: CONV, contact_id: 'ct-1', contact_name: name, contact_kind: kind, contact_phone: '+5592999990000', status: 'open' })

  function serveFull(over: { kind?: string; name?: string } = {}) {
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: conversationOf(over.kind ?? 'customer', over.name ?? 'Matheus') }
      if (url.endsWith('/contacts/ct-1')) return { data: { id: 'ct-1', display_name: over.name ?? 'Matheus', phone_e164: '+5592999990000', email: 'm@x.com', kind: over.kind ?? 'customer' } }
      if (url.endsWith('/contacts/ct-1/classification')) return { data: { kind: over.kind ?? 'customer', accounts: [] } }
      if (url.endsWith(`/conversations/${CONV}/ticket`)) return Promise.reject({ response: { status: 404 } })
      if (url.endsWith('/crm/companies')) return { data: { items: [] } }
      return Promise.reject({ response: { status: 404 } })
    })
  }

  it('shows the contact and the classification of the item\'s COMPANY, every request naming that company and the Hub', async () => {
    serveFull()
    renderAt(<HubContextPane detail={detail(FULL)} fullContext={false} onOpenFull={() => {}} />)
    expect(await screen.findByText('m@x.com')).toBeInTheDocument()
    expect(await screen.findByRole('group', { name: 'Tipo de contato' })).toBeInTheDocument()
    const calls = vi.mocked(axios.get).mock.calls.filter(([u]) => String(u).includes('/tenants/'))
    expect(calls.length).toBeGreaterThan(2)
    for (const [url, config] of calls) {
      expect(String(url)).toContain(`/tenants/${COMPANY}/`) // never the session's own tenant
      expect((config as any).headers['X-Omnira-Acting-As']).toBe(`hub:${HUB}`)
    }
  })

  it('classifies through the Hub: the change goes to the item\'s company with the acting header', async () => {
    serveFull({ kind: 'unclassified' })
    vi.mocked(axios.put).mockResolvedValue({ data: { kind: 'other', accounts: [] } })
    renderAt(<HubContextPane detail={detail(FULL)} fullContext={false} onOpenFull={() => {}} />)
    const other = await screen.findByRole('button', { name: 'Outros' })
    fireEvent.click(other)
    await waitFor(() => expect(vi.mocked(axios.put)).toHaveBeenCalled())
    const [url, body, config] = vi.mocked(axios.put).mock.calls[0] as [string, any, any]
    expect(url).toContain(`/tenants/${COMPANY}/contacts/ct-1/classification`)
    expect(body).toEqual({ kind: 'other' })
    expect(config.headers['X-Omnira-Acting-As']).toBe(`hub:${HUB}`)
  })

  it('offers the ERP ticket only with a ticket key, and the classification only with contact.classify', async () => {
    serveFull()
    renderAt(<HubContextPane detail={detail(['conversation.read', 'contact.read'])} fullContext={false} onOpenFull={() => {}} />)
    await screen.findByText('m@x.com')
    expect(screen.queryByRole('group', { name: 'Tipo de contato' })).not.toBeInTheDocument()
    expect(screen.queryByText('Chamado')).not.toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).includes('/ticket'))).toBe(false)
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).includes('/classification'))).toBe(false)
  })

  it('what was loaded for one company never answers for another (Tenant A / Tenant B)', async () => {
    const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
    const ui = (d: HubItemDetail) => (
      <QueryClientProvider client={client}>
        <MemoryRouter><HubContextPane detail={d} fullContext={false} onOpenFull={() => {}} /></MemoryRouter>
      </QueryClientProvider>
    )
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      const company = String(url).includes('/tenants/company-b/') ? 'B' : 'A'
      if (url.endsWith('/inbox/conversations/conv-1')) return { data: conversationOf('customer', `Contato da empresa ${company}`) }
      if (url.endsWith('/contacts/ct-1')) return { data: { id: 'ct-1', display_name: `Contato da empresa ${company}`, kind: 'customer' } }
      if (url.endsWith('/classification')) return { data: { kind: 'customer', accounts: [] } }
      return Promise.reject({ response: { status: 404 } })
    })
    const view = render(ui(detail(['conversation.read', 'contact.read'], 'company-a')))
    expect(await screen.findByText('Contato da empresa A')).toBeInTheDocument()
    // the same conversation id on another company (ids are per company): the Hub switches the item
    view.rerender(ui(detail(['conversation.read', 'contact.read'], 'company-b')))
    expect(await screen.findByText('Contato da empresa B')).toBeInTheDocument()
    expect(screen.queryByText('Contato da empresa A')).not.toBeInTheDocument()
  })
})
