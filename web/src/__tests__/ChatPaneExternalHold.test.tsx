import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ChatPane from '../components/inbox/ChatPane'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')
vi.mock('../hooks/useRealtimeEvents', () => ({ useRealtimeEvents: () => undefined }))

const CONV = 'conv-1'

function serve(opts: { control?: { kind: string; origin?: string; version: number }; permissions: string[]; takeover?: 'ok' | 409 }) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: opts.permissions } }
    if (url.endsWith(`/inbox/conversations/${CONV}`)) {
      return { data: { id: CONV, contact_name: 'Maria', contact_phone: '+5511999990000', status: 'open', ...(opts.control ? { control: opts.control } : {}) } }
    }
    if (url.endsWith('/messages')) return { data: { items: [], has_more: false } }
    return Promise.reject({ response: { status: 404 } })
  })
  vi.mocked(axios.post).mockImplementation(async (url: string) => {
    if (url.endsWith('/takeover')) {
      if (opts.takeover === 409) return Promise.reject({ response: { status: 409 } })
      return { data: { conversation_id: CONV, control_kind: 'tenant_user', control_version: 3 } }
    }
    return Promise.reject({ response: { status: 404 } })
  })
}

describe('ChatPane — a conversation held by an external application (CRM)', () => {
  beforeEach(() => {
    vi.resetAllMocks()
    localStorage.clear()
    setSession()
  })

  it('does not offer to claim or to reply, names only the kind of holder, and lets the person refresh', async () => {
    serve({ control: { kind: 'crm_application', origin: 'crm', version: 2 }, permissions: ['conversation.claim'] })
    renderAt(<ChatPane conversationId={CONV} />)
    expect(await screen.findByText(/sendo atendida por uma/)).toBeInTheDocument()
    expect(screen.getByText('Aplicação externa (CRM)')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Assumir/ })).not.toBeInTheDocument()
    expect(screen.queryByLabelText(/Responder ao contato/)).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Retomar atendimento' })).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Atualizar conversa' })).toBeInTheDocument()
  })

  it('a supervisor takes it back only with a reason and a confirmation', async () => {
    serve({ control: { kind: 'crm_application', origin: 'crm', version: 2 }, permissions: ['conversation.takeover_external'] })
    renderAt(<ChatPane conversationId={CONV} />)
    await userEvent.click(await screen.findByRole('button', { name: 'Retomar atendimento' }))
    const confirmButton = await screen.findAllByRole('button', { name: 'Retomar atendimento' })
    const submit = confirmButton[confirmButton.length - 1]
    expect(submit).toBeDisabled()
    await userEvent.type(screen.getByLabelText(/Motivo/), 'cliente pediu atendimento humano')
    expect(submit).toBeDisabled() // the confirmation is missing
    await userEvent.click(screen.getByRole('checkbox'))
    expect(submit).toBeEnabled()
    await userEvent.click(submit)
    await waitFor(() => expect(vi.mocked(axios.post)).toHaveBeenCalled())
    const [url, body] = vi.mocked(axios.post).mock.calls[0]
    expect(String(url)).toMatch(/\/inbox\/conversations\/conv-1\/takeover$/)
    expect(body).toEqual({ reason: 'cliente pediu atendimento humano', confirm: true })
  })

  it('says so when the conversation changed under the supervisor (409) and never retries by itself', async () => {
    serve({ control: { kind: 'crm_application', version: 2 }, permissions: ['conversation.takeover_external'], takeover: 409 })
    renderAt(<ChatPane conversationId={CONV} />)
    await userEvent.click(await screen.findByRole('button', { name: 'Retomar atendimento' }))
    await userEvent.type(await screen.findByLabelText(/Motivo/), 'motivo suficiente')
    await userEvent.click(screen.getByRole('checkbox'))
    const buttons = screen.getAllByRole('button', { name: 'Retomar atendimento' })
    await userEvent.click(buttons[buttons.length - 1])
    expect(await screen.findByText(/A conversa mudou enquanto você decidia/)).toBeInTheDocument()
    expect(vi.mocked(axios.post)).toHaveBeenCalledTimes(1)
  })

  it('shows the Workspace composer as before when nobody external holds it', async () => {
    serve({ control: { kind: 'tenant_user', version: 1 }, permissions: ['conversation.claim'] })
    renderAt(<ChatPane conversationId={CONV} />)
    await screen.findByText('Maria')
    expect(screen.queryByText(/sendo atendida por uma/)).not.toBeInTheDocument()
  })
})
