import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { act, render, screen, waitFor } from '@testing-library/react'
import axios from 'axios'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import DelegatedContextPane from '../components/inbox/DelegatedContextPane'
import { setActing, clearActing } from '../lib/acting'
import { setSession } from './testUtils'

// Phase 04c: handing over / giving back a conversation the person holds, and the notes of the contact, are offered to a Hub agent only when the SERVER says
// (through /me/access, which answers with the DELEGATED keys in this context) that the keys are held. The server still decides every request.
const get = vi.spyOn(axios, 'get')
const post = vi.spyOn(axios, 'post')

function serve(keys: string[], conversation: Record<string, unknown> = { assigned_to_user_id: 'u-1', assigned_to_name: 'Operador Teste' }) {
  get.mockImplementation(async (url: string) => {
    const u = String(url)
    if (u.endsWith('/me/access')) return { data: { role_key: 'hub_delegate', permissions: keys } }
    if (u.endsWith('/inbox/conversations/c1')) return { data: { id: 'c1', contact_name: 'Berjon Brito', status: 'open', contact_id: 'k1', contact_kind: 'customer', ...conversation } }
    if (u.endsWith('/contacts/k1')) return { data: { id: 'k1', display_name: 'Berjon Brito', kind: 'customer' } }
    if (u.endsWith('/transfer-candidates')) return { data: { items: [{ user_id: 'u-2', name: 'Colega Um', email: 'colega@example.com', load: 3 }] } }
    if (u.endsWith('/contacts/k1/notes')) return { data: { items: [{ id: 'n1', body: 'Prefere contato à tarde', author_user_id: 'u-9', author_name: '', created_at: '2026-10-10T10:00:00Z', updated_at: '2026-10-10T10:00:00Z', mine: false }] } }
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

beforeEach(() => {
  localStorage.clear()
  setSession()
  setActing('hub-1', 'Beta Hub')
  get.mockReset()
  post.mockReset()
})
afterEach(() => clearActing())

describe('handing over a conversation while attending through the Hub (phase 04c)', () => {
  it('offers the hub colleagues the server lists, and transfers to the one picked', async () => {
    serve(['conversation.read', 'conversation.claim', 'conversation.transfer', 'contact.read'])
    post.mockResolvedValue({ data: { changed: true } })
    show()
    {
      const el = await screen.findByRole('button', { name: 'Transferir ou devolver' })
      await act(async () => el.click())
    }
    const pick = await screen.findByRole('radio', { name: 'Colega Um' })
    await act(async () => pick.click())
    await act(async () => screen.getByRole('button', { name: 'Transferir' }).click())
    await waitFor(() => expect(post).toHaveBeenCalled())
    const [url, body] = post.mock.calls[0]
    expect(String(url)).toMatch(/\/inbox\/conversations\/c1\/transfer$/)
    expect(body).toEqual({ target_user_id: 'u-2' })
  })

  it('gives the conversation back to the queue', async () => {
    serve(['conversation.read', 'conversation.claim', 'conversation.transfer', 'contact.read'])
    post.mockResolvedValue({ data: { changed: true } })
    show()
    {
      const el = await screen.findByRole('button', { name: 'Transferir ou devolver' })
      await act(async () => el.click())
    }
    {
      const el = await screen.findByRole('button', { name: 'Devolver à fila' })
      await act(async () => el.click())
    }
    await waitFor(() => expect(post).toHaveBeenCalled())
    expect(String(post.mock.calls[0][0])).toMatch(/\/inbox\/conversations\/c1\/unassign$/)
  })

  it('with only the claim key it can give back but never lists or picks people', async () => {
    serve(['conversation.read', 'conversation.claim', 'contact.read'])
    show()
    {
      const el = await screen.findByRole('button', { name: 'Transferir ou devolver' })
      await act(async () => el.click())
    }
    expect(await screen.findByText(/não escolher quem a recebe/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Transferir' })).not.toBeInTheDocument()
    expect(get.mock.calls.map((c) => String(c[0])).some((u) => u.endsWith('/transfer-candidates'))).toBe(false)
  })

  it('shows the server reason when the transfer is refused', async () => {
    serve(['conversation.read', 'conversation.claim', 'conversation.transfer', 'contact.read'])
    post.mockRejectedValue({ response: { status: 409, data: 'transfer target not available: that person cannot take this conversation now' } })
    show()
    {
      const el = await screen.findByRole('button', { name: 'Transferir ou devolver' })
      await act(async () => el.click())
    }
    {
      const el = await screen.findByRole('radio', { name: 'Colega Um' })
      await act(async () => el.click())
    }
    await act(async () => screen.getByRole('button', { name: 'Transferir' }).click())
    expect(await screen.findByRole('alert')).toHaveTextContent('Essa pessoa não pode receber esta conversa agora')
  })

  it('offers nothing when the conversation is not held by the person, or without the keys', async () => {
    serve(['conversation.read', 'conversation.claim', 'conversation.transfer', 'contact.read'], { assigned_to_user_id: 'u-7', assigned_to_name: 'Outra pessoa' })
    show()
    expect(await screen.findByText('Outra pessoa')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Transferir ou devolver' })).not.toBeInTheDocument()
  })

  it('offers nothing without conversation.transfer or conversation.claim', async () => {
    serve(['conversation.read', 'contact.read'])
    show()
    expect(await screen.findByText('Operador Teste')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Transferir ou devolver' })).not.toBeInTheDocument()
  })
})

describe('the notes of the contact while attending through the Hub (phase 04c)', () => {
  it('shows the notes with contact.note, even when the author name is not readable', async () => {
    serve(['conversation.read', 'contact.read', 'contact.note'])
    show()
    expect(await screen.findByText('Prefere contato à tarde')).toBeInTheDocument()
    expect(screen.getByRole('region', { name: 'Anotações do contato' })).toBeInTheDocument()
  })

  it('does not ask for the notes at all without contact.note', async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify', 'account.read'])
    show()
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(screen.queryByRole('region', { name: 'Anotações do contato' })).not.toBeInTheDocument()
    expect(get.mock.calls.map((c) => String(c[0])).some((u) => u.endsWith('/notes'))).toBe(false)
  })
})

describe('the AI summary while attending through the Hub (phase 05)', () => {
  it('offers the summary only with ai.assist, and asks the server for it', async () => {
    serve(['conversation.read', 'contact.read', 'ai.assist'])
    post.mockResolvedValue({ data: { summary: 'Cliente sem internet desde ontem.' } })
    show()
    const button = await screen.findByRole('button', { name: /resum/i })
    await act(async () => button.click())
    expect(await screen.findByText('Cliente sem internet desde ontem.')).toBeInTheDocument()
    expect(String(post.mock.calls[0][0])).toMatch(/\/conversations\/c1\/ai\/summary$/)
  })

  it('shows nothing about it without ai.assist', async () => {
    serve(['conversation.read', 'contact.read', 'contact.classify'])
    show()
    expect(await screen.findByText('Berjon Brito')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /resum/i })).not.toBeInTheDocument()
  })
})
