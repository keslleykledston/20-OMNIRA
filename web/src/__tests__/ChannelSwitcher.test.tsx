import { describe, it, expect, vi, beforeEach } from 'vitest'
import { render, screen, fireEvent, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import axios from 'axios'
import { ChannelSwitcher } from '../components/inbox/ChannelSwitcher'

vi.mock('axios')
const mocked = vi.mocked(axios, true)

const lines = [
  { id: 'l-waha', provider: 'waha', provider_kind: 'unofficial', label: 'WhatsApp · +559291882864', number: '+559291882864', status: 'active', can_send_text: true, window_required: false },
  { id: 'l-meta', provider: 'meta_cloud', provider_kind: 'official', label: 'WhatsApp oficial · +55 92 98451-7378', number: '+55 92 98451-7378', status: 'active', can_send_text: true, window_required: true },
]

describe('ChannelSwitcher', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.setItem('tenantId', 't1')
    mocked.get.mockResolvedValue({ data: { items: lines } })
  })

  it('shows the current line and opens the person on another one without sending anything', async () => {
    mocked.post.mockResolvedValue({ data: { conversation_id: 'conv-meta', created: true } })
    const onOpen = vi.fn()
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ChannelSwitcher contactId="c1" currentChannelId="l-waha" onOpenConversation={onOpen} />
      </QueryClientProvider>,
    )
    expect(await screen.findByText('WhatsApp · +559291882864')).toBeInTheDocument()
    fireEvent.click(await screen.findByRole('button', { name: /WhatsApp oficial/ }))
    await waitFor(() => expect(onOpen).toHaveBeenCalledWith('conv-meta'))
    expect(mocked.post).toHaveBeenCalledTimes(1)
    expect(String(mocked.post.mock.calls[0][0])).toMatch(/inbox\/conversations\/open$/)
    expect(mocked.post.mock.calls[0][1]).toEqual({ contact_id: 'c1', channel_connection_id: 'l-meta' })
  })

  it('says why when the contact cannot be reached on that line', async () => {
    mocked.post.mockRejectedValue({ response: { status: 422 } })
    render(
      <QueryClientProvider client={new QueryClient()}>
        <ChannelSwitcher contactId="c1" currentChannelId="l-waha" onOpenConversation={() => {}} />
      </QueryClientProvider>,
    )
    fireEvent.click(await screen.findByRole('button', { name: /WhatsApp oficial/ }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/não pode ser contatado/)
  })
})
