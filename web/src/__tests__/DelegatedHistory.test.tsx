import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, fireEvent, render, screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import DelegatedContextPane from '../components/inbox/DelegatedContextPane'
import { setActing, clearActing } from '../lib/acting'
import { setSession } from './testUtils'

// Phase 06: the memory of the contact (earlier attendances and open items) is offered to a Hub agent only when the SERVER says (through /me/access, which
// answers with the DELEGATED keys in this context) that attendance.history is held; marking an item done or dropped needs followup.resolve as well. The server
// still decides every request.
const get = vi.spyOn(axios, 'get')
const post = vi.spyOn(axios, 'post')

const history = {
  contact_id: 'k1',
  attendances: [
    { id: 'cl1', conversation_id: 'c0', closed_by_user_id: null, source: 'agent', reason: 'resolved', note: '', summary: 'Cliente pediu segunda via do boleto.', summary_truth: 'agent_confirmed', local_tickets_closed: 0, tickets_kept: 1, created_at: '2026-10-01T10:00:00Z', follow_ups: [] },
  ],
  open_follow_ups: [
    { id: 'f1', conversation_id: 'c0', kind: 'pending', text: 'ligar para confirmar a instalação', owner_user_id: null, due_at: null, status: 'open', truth: 'agent_confirmed', created_at: '2026-10-01T10:00:00Z', resolved_at: null, resolution_note: '' },
  ],
}

function serve(keys: string[]) {
  get.mockImplementation(async (url: string) => {
    const u = String(url)
    if (u.endsWith('/me/access')) return { data: { role_key: 'hub_delegate', permissions: keys } }
    if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', status: 'open', contact_id: 'k1', contact_kind: 'customer', assigned_to_user_id: 'u-9' } }
    if (u.endsWith('/contacts/k1')) return { data: { id: 'k1', display_name: 'Berjon Brito', kind: 'customer' } }
    if (u.endsWith('/inbox/conversations/c1/attendance-context')) return { data: history }
    if (u.endsWith('/inbox/conversations/c1/history-search')) return { data: { items: [{ at: '2026-09-20T12:00:00Z', role: 'customer', snippet: 'meu boleto de março não chegou', conversation_id: 'c0' }] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

function show() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <DelegatedContextPane conversationId="c1" />
      </MemoryRouter>
    </QueryClientProvider>,
  )
}

const asked = () => get.mock.calls.map((c) => String(c[0]))

beforeEach(() => {
  localStorage.clear()
  setSession()
  setActing('hub-1', 'Beta Hub')
  get.mockReset()
  post.mockReset()
})
afterEach(() => clearActing())

describe('the memory of the contact while attending through the Hub (phase 06)', () => {
  it('with attendance.history shows the earlier attendances and the open items, read-only', async () => {
    serve(['conversation.read', 'contact.read', 'attendance.history'])
    show()
    expect(await screen.findByText('Cliente pediu segunda via do boleto.')).toBeInTheDocument()
    expect(screen.getByText('ligar para confirmar a instalação')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Concluir/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Descartar/ })).not.toBeInTheDocument()
    expect(asked().some((u) => u.endsWith('/inbox/conversations/c1/attendance-context'))).toBe(true)
  })

  it('with followup.resolve too, marks the item done through the instance route', async () => {
    serve(['conversation.read', 'contact.read', 'attendance.history', 'followup.resolve'])
    post.mockResolvedValue({ data: { id: 'f1', status: 'done' } })
    show()
    const el = await screen.findByRole('button', { name: 'Concluir: ligar para confirmar a instalação' })
    await act(async () => el.click())
    await waitFor(() => expect(post).toHaveBeenCalled())
    expect(String(post.mock.calls[0][0])).toMatch(/\/follow-ups\/f1\/resolve$/)
    expect(post.mock.calls[0][1]).toEqual({ status: 'done' })
  })

  it('without the key it asks nothing and says it is not released', async () => {
    serve(['conversation.read', 'contact.read', 'contact.note', 'followup.resolve'])
    show()
    expect(await screen.findByRole('note')).toHaveTextContent('não está liberado para você')
    expect(asked().some((u) => u.includes('attendance-context') || u.includes('attendance-history'))).toBe(false)
    expect(screen.queryByText('Cliente pediu segunda via do boleto.')).not.toBeInTheDocument()
  })

  it('with attendance.history and conversation.read offers the search over the earlier messages of the contact (phase 09)', async () => {
    serve(['conversation.read', 'contact.read', 'attendance.history'])
    show()
    const box = await screen.findByLabelText('Buscar no histórico do contato')
    await act(async () => {
      fireEvent.change(box, { target: { value: 'boleto' } })
    })
    await act(async () => screen.getByRole('button', { name: 'Buscar' }).click())
    expect(await screen.findByText('meu boleto de março não chegou')).toBeInTheDocument()
    const call = get.mock.calls.find((c) => String(c[0]).endsWith('/inbox/conversations/c1/history-search'))
    expect(call).toBeTruthy()
    expect((call![1] as { params: { q: string } }).params.q).toBe('boleto')
  })

  it('without conversation.read the search is not offered even with the memory key, and nothing is asked', async () => {
    serve(['contact.read', 'attendance.history'])
    show()
    await screen.findByText('Cliente pediu segunda via do boleto.')
    expect(screen.queryByLabelText('Buscar no histórico do contato')).not.toBeInTheDocument()
    expect(asked().some((u) => u.includes('history-search'))).toBe(false)
  })

  it('without attendance.history there is no search either', async () => {
    serve(['conversation.read', 'contact.read'])
    show()
    expect(await screen.findByRole('note')).toHaveTextContent('não está liberado')
    expect(screen.queryByLabelText('Buscar no histórico do contato')).not.toBeInTheDocument()
  })
})
