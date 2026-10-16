import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import HubContextPane from '../components/hub/HubContextPane'
import { renderAt, setSession } from './testUtils'
import type { HubItemDetail } from '../lib/hub'

vi.mock('axios')

const HUB = 'hub-1'
const COMPANY = 'company-k3g'
const CONV = 'conv-1'

const detail = (serviceKeys?: string[]): HubItemDetail => ({
  item: { id: 'item-1', tenant_id: COMPANY, tenant_name: 'K3G_Solutions', conversation_id: CONV, customer_name: 'Matheus Oliveira', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 } as any,
  tenant: { id: COMPANY, name: 'K3G_Solutions' },
  access: { source: 'hub', hub_id: HUB, can_reply: true, ...(serviceKeys ? { service_keys: serviceKeys } : {}) },
  conversation: { id: CONV, status: 'open', created_at: '2026-10-15T12:00:00Z', assignment: 'me' },
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
})
