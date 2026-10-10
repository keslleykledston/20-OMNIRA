import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import { MemoryRouter } from 'react-router-dom'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'
import FinalizeDialog from '../components/inbox/FinalizeDialog'
import { setActing, clearActing } from '../lib/acting'
import { getTenantId } from '../lib/session'
import { setSession } from './testUtils'

// Phase 05b: ending the attendance while attending an instance through the Hub. The dialog is the member's, minus what is not part of the delegated context
// (the channel lookup; the AI draft only with ai.assist, phase 07); the closing notice goes out through the reply route when the person may reply.
const get = vi.spyOn(axios, 'get')
const post = vi.spyOn(axios, 'post')

function show(keys: string[]) {
  get.mockImplementation(async (url: string) => {
    if (String(url).endsWith('/me/access')) return { data: { role_key: 'hub_delegate', permissions: keys } }
    return Promise.reject({ response: { status: 404 } })
  })
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } })
  client.setQueryData(['inbox-conversation-detail', getTenantId(), 'c1'], { id: 'c1', status: 'open', assigned_to_user_id: 'u-1' })
  return render(
    <QueryClientProvider client={client}>
      <MemoryRouter>
        <FinalizeDialog open conversationId="c1" contactName="Maria" onClose={() => undefined} />
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

describe('finalizing the attendance through the Hub (phase 05b)', () => {
  it('does not offer the AI draft without ai.assist, never asks for the channel, and finalizes through the instance route', async () => {
    show(['conversation.read', 'conversation.reply', 'conversation.finalize'])
    post.mockResolvedValue({ data: { changed: true } })
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog', { name: 'Finalizar atendimento' })
    expect(within(dialog).queryByRole('button', { name: 'Sugerir com IA' })).not.toBeInTheDocument()
    expect(get.mock.calls.map((c) => String(c[0])).some((u) => u.endsWith('/channel'))).toBe(false)
    await user.type(within(dialog).getByLabelText('Resumo do atendimento'), 'Cliente voltou a navegar')
    // the closing notice is offered because the person may reply
    const notify = await within(dialog).findByRole('checkbox', { name: /Avisar o cliente/ })
    await user.click(notify) // finalize without notifying: this test is about the finalize call
    await user.click(within(dialog).getByRole('button', { name: 'Finalizar atendimento' }))
    await waitFor(() => expect(post.mock.calls.some((c) => String(c[0]).endsWith('/inbox/conversations/c1/finalize'))).toBe(true))
    const body = post.mock.calls.find((c) => String(c[0]).endsWith('/finalize'))![1] as Record<string, unknown>
    expect(body).toMatchObject({ reason: 'resolved', summary: 'Cliente voltou a navegar' })
  })

  it('sends the closing notice through the reply route first, once', async () => {
    show(['conversation.read', 'conversation.reply', 'conversation.finalize'])
    post.mockResolvedValue({ data: { changed: true } })
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog', { name: 'Finalizar atendimento' })
    await within(dialog).findByRole('checkbox', { name: /Avisar o cliente/ })
    await user.click(within(dialog).getByRole('button', { name: 'Finalizar atendimento' }))
    await waitFor(() => expect(post.mock.calls.some((c) => String(c[0]).endsWith('/finalize'))).toBe(true))
    const urls = post.mock.calls.map((c) => String(c[0]))
    expect(urls.findIndex((u) => u.endsWith('/messages'))).toBeLessThan(urls.findIndex((u) => u.endsWith('/finalize')))
  })

  it('offers no closing notice without the reply key', async () => {
    show(['conversation.read', 'conversation.finalize'])
    const dialog = await screen.findByRole('dialog', { name: 'Finalizar atendimento' })
    await waitFor(() => expect(get).toHaveBeenCalled())
    expect(within(dialog).queryByRole('checkbox', { name: /Avisar o cliente/ })).not.toBeInTheDocument()
  })

  it('with ai.assist offers the draft, asks the instance route and fills the summary and the items for the person to check', async () => {
    show(['conversation.read', 'conversation.reply', 'conversation.finalize', 'ai.assist'])
    post.mockResolvedValue({ data: { summary: 'Cliente relatou queda de conexão.', summary_truth: 'ai_inferred', follow_ups: [{ kind: 'pending', text: 'confirmar visita' }], model: 'fake', based_on_messages: 3 } })
    const user = userEvent.setup()
    const dialog = await screen.findByRole('dialog', { name: 'Finalizar atendimento' })
    await user.click(await within(dialog).findByRole('button', { name: 'Sugerir com IA' }))
    await waitFor(() => expect(post.mock.calls.some((c) => String(c[0]).endsWith('/inbox/conversations/c1/finalize/suggest'))).toBe(true))
    expect(await within(dialog).findByDisplayValue('Cliente relatou queda de conexão.')).toBeInTheDocument()
    expect(within(dialog).getByText(/Sugerido pela IA: confira antes de finalizar/)).toBeInTheDocument()
    expect(post.mock.calls.some((c) => String(c[0]).endsWith('/finalize'))).toBe(false) // a draft never finalizes
  })
})
