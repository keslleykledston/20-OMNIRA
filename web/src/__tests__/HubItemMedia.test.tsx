import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import HubItemView from '../components/hub/HubItemView'
import { renderAt, setSession } from './testUtils'
import type { HubItemDetail } from '../lib/hub'

vi.mock('axios')

const HUB = 'hub-1'
const COMPANY = 'company-k3g'

const detail = (over: Partial<HubItemDetail['conversation']> = {}): HubItemDetail => ({
  item: { id: 'item-1', tenant_id: COMPANY, tenant_name: 'K3G_Solutions', conversation_id: 'conv-1', customer_name: 'Matheus', channel: 'whatsapp', status: 'open', priority: 'normal', unread_count: 0 } as any,
  tenant: { id: COMPANY, name: 'K3G_Solutions' },
  access: { source: 'hub', hub_id: HUB, can_reply: true },
  conversation: { id: 'conv-1', status: 'open', created_at: '2026-10-15T12:00:00Z', assignment: 'me', ...over },
  messages: [],
})

const channel = (canMedia: boolean) => ({ provider: 'waha', can_send_text: true, can_send_media: canMedia, window_required: false, window_open: true })

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('HubItemView — files from the Hub\'s composer (ADR-0024 x ADR-0040)', () => {
  it('offers the paperclip only when the server says the line can send files, asking for the item\'s company', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: channel(false) })
    renderAt(<HubItemView hubId={HUB} detail={detail()} />)
    await screen.findByPlaceholderText('Escreva uma mensagem...')
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled())
    const [url, config] = vi.mocked(axios.get).mock.calls[0] as [string, any]
    expect(url).toContain(`/hubs/${HUB}/inbox/item-1/channel`)
    expect(config.params).toEqual({ expected_tenant_id: COMPANY })
    expect(screen.queryByRole('button', { name: 'Anexar arquivo' })).not.toBeInTheDocument()
  })

  it('uploads through the Hub, then sends the file with its caption in one message', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: channel(true) })
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/attachments')) return { data: { id: 'att-1', kind: 'image', mime: 'image/png', size_bytes: 1200, file_name: 'foto.png', expires_at: '2026-10-17T00:00:00Z' } }
      return { data: { id: 'm1' } }
    })
    const user = userEvent.setup()
    renderAt(<HubItemView hubId={HUB} detail={detail()} />)
    await screen.findByRole('button', { name: 'Anexar arquivo' })
    const file = new File([new Uint8Array(1200)], 'foto.png', { type: 'image/png' })
    fireEvent.change(screen.getByTestId('attachment-input'), { target: { files: [file] } })
    expect(await screen.findByText('foto.png')).toBeInTheDocument()
    const up = vi.mocked(axios.post).mock.calls.find(([u]) => String(u).endsWith('/attachments'))!
    expect(String(up[0])).toContain(`/hubs/${HUB}/inbox/item-1/attachments`)
    expect((up[1] as FormData).get('file')).toBeInstanceOf(File)
    expect((up[2] as any).params).toEqual({ expected_tenant_id: COMPANY })
    await user.type(screen.getByPlaceholderText('Legenda (opcional)...'), 'segue a foto')
    await user.click(screen.getByRole('button', { name: 'Enviar mensagem' }))
    await waitFor(() => expect(vi.mocked(axios.post).mock.calls.some(([u]) => String(u).endsWith('/messages'))).toBe(true))
    const send = vi.mocked(axios.post).mock.calls.find(([u]) => String(u).endsWith('/messages'))!
    expect(String(send[0])).toContain(`/hubs/${HUB}/inbox/item-1/messages`)
    expect(send[1]).toEqual({ expected_tenant_id: COMPANY, text: 'segue a foto', attachment_id: 'att-1' })
    expect((send[2] as any).headers['Idempotency-Key']).toBeTruthy()
  })

  it('says why a file was refused and sends nothing', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: channel(true) })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422, data: 'the file was blocked by the antivirus\n' } })
    renderAt(<HubItemView hubId={HUB} detail={detail()} />)
    await screen.findByRole('button', { name: 'Anexar arquivo' })
    fireEvent.change(screen.getByTestId('attachment-input'), { target: { files: [new File(['x'], 'a.txt', { type: 'text/plain' })] } })
    expect(await screen.findByText('O antivírus bloqueou este arquivo.')).toBeInTheDocument()
    expect(vi.mocked(axios.post).mock.calls.some(([u]) => String(u).endsWith('/messages'))).toBe(false)
  })

  it('the paperclip never shows for a conversation the agent does not hold', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: channel(true) })
    renderAt(<HubItemView hubId={HUB} detail={detail({ assignment: 'none' })} />)
    expect(await screen.findByText(/Assuma a conversa/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Anexar arquivo' })).not.toBeInTheDocument()
    expect(vi.mocked(axios.get)).not.toHaveBeenCalled()
  })
})

describe('HubItemView — the protocol beside the customer name', () => {
  const withKeys = (keys: string[]): HubItemDetail => ({ ...detail(), access: { source: 'hub', hub_id: HUB, can_reply: true, keys } as any })
  const answer = () =>
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/channel')) return { data: channel(false) }
      if (url.endsWith('/service-session')) return { data: { id: 's1', protocol: 'KTG-1005', status: 'in_progress', version: 1 } }
      return Promise.reject({ response: { status: 404 } })
    })

  it('shows it when the grant reads the service context, asking the item\'s company as that Hub', async () => {
    answer()
    renderAt(<HubItemView hubId={HUB} detail={withKeys(['service.read'])} />)
    expect(await screen.findByTestId('chat-protocol')).toHaveTextContent('KTG-1005')
    const call = vi.mocked(axios.get).mock.calls.find(([u]) => String(u).endsWith('/service-session')) as [string, any]
    expect(call[0]).toContain(`/tenants/${COMPANY}/conversations/conv-1/service-session`)
    expect(call[1].headers['X-Omnira-Acting-As']).toBe(`hub:${HUB}`)
  })

  it('without service.read in the grant it does not even ask', async () => {
    answer()
    renderAt(<HubItemView hubId={HUB} detail={withKeys(['conversation.reply'])} />)
    await screen.findByPlaceholderText('Escreva uma mensagem...')
    await waitFor(() => expect(vi.mocked(axios.get)).toHaveBeenCalled())
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/service-session'))).toBe(false)
    expect(screen.queryByTestId('chat-protocol')).not.toBeInTheDocument()
  })
})
