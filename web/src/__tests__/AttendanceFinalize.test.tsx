import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import ContextPane from '../components/inbox/ContextPane'
import ChatPane from '../components/inbox/ChatPane'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')
vi.mock('../hooks/useRealtimeEvents', () => ({ useRealtimeEvents: () => undefined }))

const CONV = 'c-1'
const base = { id: CONV, contact_name: 'Maria Silva', contact_phone: '+5511999998888', status: 'open', message_count: 3, assigned_to_user_id: 'user-1' }

const history = {
  contact_id: 'ct-1',
  attendances: [{ id: 'cl-1', conversation_id: 'c-0', closed_by_user_id: 'u', source: 'agent', reason: 'resolved', note: '', summary: 'Link voltou depois de reiniciar a ONU.', summary_truth: 'agent_confirmed', local_tickets_closed: 1, tickets_kept: 0, created_at: '2026-10-01T12:00:00Z', follow_ups: [] }],
  open_follow_ups: [
    { id: 'f-1', conversation_id: 'c-0', kind: 'promise', text: 'Ligar com o resultado da visita', owner_user_id: null, due_at: '2020-01-01T12:00:00Z', status: 'open', truth: 'agent_confirmed', created_at: '2026-10-01T12:00:00Z', resolved_at: null, resolution_note: '' },
    { id: 'f-2', conversation_id: 'c-0', kind: 'pending', text: 'Enviar segunda via', owner_user_id: null, due_at: null, status: 'open', truth: 'agent_confirmed', created_at: '2026-10-01T12:00:00Z', resolved_at: null, resolution_note: '' },
  ],
}

function serve(over: { conversation?: Record<string, unknown>; history?: unknown } = {}) {
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith(`/inbox/conversations/${CONV}`)) return { data: { ...base, ...over.conversation } }
    if (url.endsWith('/attendance-context')) {
      if (over.history === undefined) return Promise.reject({ response: { status: 404 } })
      return { data: over.history }
    }
    if (url.endsWith('/crm/companies')) return { data: { items: [] } }
    if (url.endsWith('/ticket')) return { data: { local_ticket_id: 'lt-1', linked: false } }
    if (url.endsWith('/messages')) return { data: { items: [], has_more: false } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('Finalizar atendimento (ADR-0020)', () => {
  it('opens the dialog, sends reason, summary and the listed items, and closes on success', async () => {
    serve()
    vi.mocked(axios.post).mockResolvedValue({ data: { changed: true } })
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await user.click(await screen.findByRole('button', { name: /Finalizar atendimento/ }))
    const dialog = await screen.findByRole('dialog', { name: 'Finalizar atendimento' })
    expect(within(dialog).getByText(/abre-se um atendimento novo/)).toBeInTheDocument()
    await user.selectOptions(within(dialog).getByLabelText('Motivo'), 'no_response')
    await user.type(within(dialog).getByLabelText('Resumo do atendimento'), 'Cliente não retornou')
    await user.click(within(dialog).getByRole('button', { name: 'Adicionar item' }))
    await user.selectOptions(within(dialog).getByLabelText('Tipo do item 1'), 'promise')
    await user.type(within(dialog).getByLabelText('Texto do item 1'), 'Ligar amanhã')
    await user.click(within(dialog).getByRole('button', { name: 'Finalizar atendimento' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    const [url, body] = vi.mocked(axios.post).mock.calls[0] as [string, any]
    expect(url).toMatch(new RegExp(`/inbox/conversations/${CONV}/finalize$`))
    expect(body).toMatchObject({ reason: 'no_response', summary: 'Cliente não retornou', follow_ups: [{ kind: 'promise', text: 'Ligar amanhã', due_at: null }] })
    await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull())
  })

  it('ignores blank items and keeps the dialog open with the backend refusal in plain words', async () => {
    serve()
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 403, data: { error: 'forbidden' } } })
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await user.click(await screen.findByRole('button', { name: /Finalizar atendimento/ }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Adicionar item' })) // left blank
    await user.click(within(dialog).getByRole('button', { name: 'Finalizar atendimento' }))
    expect(await within(dialog).findByRole('alert')).toHaveTextContent(/responsável pela conversa/)
    expect((vi.mocked(axios.post).mock.calls[0] as any)[1].follow_ups).toEqual([])
    expect(screen.getByRole('dialog')).toBeInTheDocument()
  })

  it('shows no finalize action once the attendance is finalized, and says so', async () => {
    serve({ conversation: { status: 'closed' } })
    renderAt(<ContextPane conversationId={CONV} />)
    expect(await screen.findByText('Atendimento finalizado.')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Finalizar atendimento/ })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Transferir' })).toBeNull()
  })

  it('does not offer to finalize a staff (internal) conversation', async () => {
    serve({ conversation: { conversation_kind: 'internal' } })
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria Silva')
    expect(screen.queryByRole('button', { name: /Finalizar atendimento/ })).toBeNull()
  })

  it('disables the composer of a finalized conversation and explains why', async () => {
    serve({ conversation: { status: 'closed' } })
    renderAt(<ChatPane conversationId={CONV} />)
    expect(await screen.findByText(/Atendimento finalizado\. Se o contato escrever de novo/)).toBeInTheDocument()
    expect(screen.queryByPlaceholderText('Escreva uma resposta...')).toBeNull()
  })
})

describe('Histórico e pendências do contato', () => {
  it('shows what is pending (overdue first) and the previous attendances with their summary', async () => {
    serve({ history })
    renderAt(<ContextPane conversationId={CONV} />)
    const section = await screen.findByRole('region', { name: 'Histórico do contato' })
    expect(within(section).getByText('Pendências do contato')).toBeInTheDocument()
    expect(within(section).getByText('Ligar com o resultado da visita')).toBeInTheDocument()
    expect(within(section).getByText(/Atrasada/)).toBeInTheDocument()
    expect(within(section).getByText('Atendimentos anteriores')).toBeInTheDocument()
    expect(within(section).getByText('Resolvido')).toBeInTheDocument()
    expect(within(section).getByText(/Link voltou depois de reiniciar a ONU/)).toBeInTheDocument()
  })

  it('stays out of the way when there is nothing to remember', async () => {
    serve({ history: { contact_id: 'ct-1', attendances: [], open_follow_ups: [] } })
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria Silva')
    expect(screen.queryByRole('region', { name: 'Histórico do contato' })).toBeNull()
  })

  it('completes or drops an item, and explains a conflict', async () => {
    serve({ history })
    vi.mocked(axios.post).mockResolvedValueOnce({ data: {} }).mockRejectedValueOnce({ response: { status: 409, data: { error: 'already_resolved' } } })
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await user.click(await screen.findByRole('button', { name: 'Concluir: Ligar com o resultado da visita' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/follow-ups\/f-1\/resolve$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ status: 'done' })
    await user.click(screen.getByRole('button', { name: 'Descartar: Enviar segunda via' }))
    expect(await screen.findByRole('alert')).toHaveTextContent(/já foi resolvida/)
  })

  it('stays silent when the history cannot be loaded (never blocks attending)', async () => {
    serve() // attendance-context -> 404
    renderAt(<ContextPane conversationId={CONV} />)
    expect(await screen.findByText('Maria Silva')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Histórico do contato' })).toBeNull()
  })
})

describe('Busca no histórico do contato', () => {
  it('searches only when asked, shows who said what and when, and tells when nothing matches', async () => {
    serve()
    const get = vi.mocked(axios.get)
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria Silva')
    expect(get.mock.calls.some((c) => String(c[0]).endsWith('/history-search'))).toBe(false) // nothing on open
    const section = screen.getByRole('region', { name: 'Buscar no histórico' })
    const input = within(section).getByLabelText('Buscar no histórico do contato')
    expect(within(section).getByRole('button', { name: 'Buscar' })).toBeDisabled() // needs 2+ characters
    get.mockImplementation(async (url: string, cfg?: any) => {
      if (String(url).endsWith('/history-search')) {
        return { data: { items: cfg.params.q === 'fatura' ? [{ at: '2026-09-20T12:00:00Z', role: 'customer', snippet: 'a fatura de agosto veio errada', conversation_id: 'c-0' }, { at: '2026-09-20T12:05:00Z', role: 'agent', snippet: 'vamos conferir a fatura', conversation_id: 'c-0' }] : [] } }
      }
      return { data: { ...base } }
    })
    await user.type(input, 'fatura')
    await user.click(within(section).getByRole('button', { name: 'Buscar' }))
    const list = await within(section).findByRole('list', { name: 'Resultados da busca' })
    expect(within(list).getByText('Cliente')).toBeInTheDocument()
    expect(within(list).getByText('Atendente')).toBeInTheDocument()
    expect(within(list).getByText('a fatura de agosto veio errada')).toBeInTheDocument()
    const call = get.mock.calls.find((c) => String(c[0]).endsWith('/history-search'))!
    expect(String(call[0])).toContain(`/inbox/conversations/${CONV}/`) // the contact is the conversation's: never a parameter
    expect((call[1] as any).params).toEqual({ q: 'fatura', limit: 5 })
    await user.clear(input)
    await user.type(input, 'xyz')
    await user.click(within(section).getByRole('button', { name: 'Buscar' }))
    expect(await within(section).findByText(/Nada encontrado nas conversas anteriores/)).toBeInTheDocument()
  })

  it('explains a refusal in plain words', async () => {
    serve()
    const user = userEvent.setup()
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria Silva')
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (String(url).endsWith('/history-search')) return Promise.reject({ response: { status: 400, data: { error: 'invalid', detail: 'attendance: invalid input: query looks like it contains a credential' } } })
      return { data: { ...base } }
    })
    const section = screen.getByRole('region', { name: 'Buscar no histórico' })
    await user.type(within(section).getByLabelText('Buscar no histórico do contato'), 'senha')
    await user.click(within(section).getByRole('button', { name: 'Buscar' }))
    expect(await within(section).findByRole('alert')).toHaveTextContent(/sem senhas ou chaves/)
  })

  it('is not offered for a staff (internal) conversation', async () => {
    serve({ conversation: { conversation_kind: 'internal' } })
    renderAt(<ContextPane conversationId={CONV} />)
    await screen.findByText('Maria Silva')
    expect(screen.queryByRole('region', { name: 'Buscar no histórico' })).toBeNull()
  })
})
