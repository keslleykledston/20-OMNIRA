import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import ProtocolTag from '../components/service/ProtocolTag'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const session = (over: Record<string, unknown> = {}) => ({ id: 's1', protocol: 'KTG-1005', status: 'in_progress', version: 1, ...over })

describe('ProtocolTag (the protocol beside the conversation)', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    setSession()
  })

  it('shows the protocol of the conversation', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: session() })
    renderAt(<ProtocolTag conversationId="c-1" />)
    expect(await screen.findByTestId('chat-protocol')).toHaveTextContent('Protocolo KTG-1005')
  })

  it('a closed protocol is shown muted and says so', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: session({ status: 'closed' }) })
    renderAt(<ProtocolTag conversationId="c-1" />)
    expect(await screen.findByTestId('chat-protocol')).toHaveAttribute('title', 'Atendimento com protocolo encerrado')
  })

  it('shows nothing when there is no protocol (404: none yet, or the module is off)', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 404 } })
    renderAt(<ProtocolTag conversationId="c-1" />)
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled())
    expect(screen.queryByTestId('chat-protocol')).not.toBeInTheDocument()
  })

  it('through the Hub it asks the company of the item, acting as that Hub', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: session({ protocol: 'KES-1002' }) })
    renderAt(<ProtocolTag conversationId="c-9" hub={{ hubId: 'hub-1', tenantId: 'tenant-z' }} />)
    expect(await screen.findByTestId('chat-protocol')).toHaveTextContent('KES-1002')
    const [url, cfg] = vi.mocked(axios.get).mock.calls[0] as [string, { headers: Record<string, string> }]
    expect(url).toContain('/tenants/tenant-z/conversations/c-9/service-session')
    expect(cfg.headers['X-Omnira-Acting-As']).toBe('hub:hub-1')
  })
})
