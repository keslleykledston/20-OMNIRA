import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ContextPane from '../components/inbox/ContextPane'
import { renderAt, setSession } from './testUtils'
import { emitInboxNotice } from '../lib/inboxNotice'

vi.mock('axios')
vi.mock('../lib/inboxNotice', async (orig) => ({ ...(await orig<typeof import('../lib/inboxNotice')>()), emitInboxNotice: vi.fn() }))
vi.mock('../hooks/useRealtimeEvents', () => ({ useRealtimeEvents: () => undefined }))

const CONV = 'c-1'
const queue = (id: string, name: string, over: Record<string, unknown> = {}) => ({ id, name, mode: 'manual', is_default: false, member_count: 1, available_count: 1, open_conversation_count: 0, created_at: '', updated_at: '', ...over })
const QUEUES = [queue('q-default', 'Default', { is_default: true }), queue('q-com', 'Comercial'), queue('q-sup', 'Suporte', { mode: 'round_robin', available_count: 3 })]

function serve(over: Record<string, unknown> = {}) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: { id: CONV, contact_name: 'Maria', contact_phone: '+5511999998888', status: 'open', message_count: 1, assigned_to_user_id: null, queue_id: 'q-default', ...over } }
    if (url.endsWith('/inbox/queue-options')) return { data: { items: QUEUES } }
    if (url.endsWith('/me/access')) return { data: { role_key: 'tenant_agent', permissions: ['conversation.claim'] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('Enviar a conversa para uma fila (setor)', () => {
  it('lists the queues, marks the current one, and sends the chosen one with the right route and body', async () => {
    serve()
    vi.mocked(axios.post).mockResolvedValue({ data: { conversation_id: CONV, queue_id: 'q-sup', changed: true } })
    const user = userEvent.setup()
    const onSent = vi.fn()
    renderAt(<ContextPane conversationId={CONV} onSentToQueue={onSent} />)
    await user.click(await screen.findByRole('button', { name: 'Enviar para uma fila' }))
    const dialog = await screen.findByRole('dialog', { name: 'Enviar para uma fila' })
    expect(await within(dialog).findByText('Comercial')).toBeInTheDocument()
    expect(within(dialog).getByText('fila atual')).toBeInTheDocument()
    expect(within(dialog).getByRole('radio', { name: /Default/ })).toBeDisabled() // it is already there
    expect(within(dialog).getByRole('button', { name: 'Enviar' })).toBeDisabled() // nothing chosen yet
    await user.click(within(dialog).getByRole('radio', { name: /Suporte/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Enviar' }))
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalled())
    const [url, body] = vi.mocked(axios.post).mock.calls[0] as [string, any]
    expect(url).toMatch(new RegExp(`/inbox/conversations/${CONV}/route-queue$`))
    expect(body).toEqual({ queue_id: 'q-sup' })
    await waitFor(() => expect(emitInboxNotice).toHaveBeenCalledWith(CONV, 'Conversa enviada para a fila Suporte.'))
    await waitFor(() => expect(screen.queryByRole('dialog', { name: 'Enviar para uma fila' })).not.toBeInTheDocument())
    // the screen lets go of the conversation: it may now be outside this person's queues
    expect(onSent).toHaveBeenCalledTimes(1)
  })

  it('says why a refusal happened and keeps the dialog open (a colleague holds it)', async () => {
    serve()
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 403, data: 'forbidden' } })
    const user = userEvent.setup()
    const onSentRefused = vi.fn()
    renderAt(<ContextPane conversationId={CONV} onSentToQueue={onSentRefused} />)
    await user.click(await screen.findByRole('button', { name: 'Enviar para uma fila' }))
    const dialog = await screen.findByRole('dialog', { name: 'Enviar para uma fila' })
    await user.click(await within(dialog).findByRole('radio', { name: /Comercial/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Enviar' }))
    expect(await within(dialog).findByText(/ela está com outra pessoa ou você não tem permissão/)).toBeInTheDocument()
    expect(emitInboxNotice).not.toHaveBeenCalled()
    expect(onSentRefused).not.toHaveBeenCalled()
  })

  it('a queue that vanished (422) is explained', async () => {
    serve()
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 422, data: 'x' } })
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await user.click(await screen.findByRole('button', { name: 'Enviar para uma fila' }))
    const dialog = await screen.findByRole('dialog', { name: 'Enviar para uma fila' })
    await user.click(await within(dialog).findByRole('radio', { name: /Comercial/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Enviar' }))
    expect(await within(dialog).findByText(/não existe mais ou o setor dela está desligado/)).toBeInTheDocument()
  })

  it('is not offered on a finished conversation', async () => {
    serve({ status: 'closed' })
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria')
    expect(screen.queryByRole('button', { name: 'Enviar para uma fila' })).not.toBeInTheDocument()
  })
})
