import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import TopicsPanel from '../components/topics/TopicsPanel'
import { renderAt, setSession } from './testUtils'
import type { TopicAccountContext, TopicAccountRef } from '../lib/topics'

vi.mock('axios')

const CONV = 'conv-1'
const T1 = 't-1'
const topics = [{ id: T1, title: 'Pedido 837', status: 'open', privacy_policy: 'public', source: 'rule', last_activity_at: '2026-10-04T12:00:00Z', message_count: 3, ticket_count: 0 }]

let access = ['topic.read', 'topic.manage', 'account.read']
let context: TopicAccountContext | 'off' | 'forbidden' = { status: 'none', related: [], candidates: [] }

const err = (status: number) => Promise.reject({ response: { status, data: '' } })

beforeEach(() => {
  vi.resetAllMocks()
  setSession()
  access = ['topic.read', 'topic.manage', 'account.read']
  context = { status: 'none', related: [], candidates: [] }
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { permissions: access } }
    if (url.endsWith(`/inbox/conversations/${CONV}/topics`)) return { data: { items: topics } }
    if (url.endsWith(`/inbox/conversations/${CONV}/ambiguities`)) return { data: { items: [] } }
    if (url.endsWith('/account-context')) {
      if (context === 'off') return err(404)
      if (context === 'forbidden') return err(403)
      return { data: context }
    }
    return err(404)
  })
})

const open = async () => {
  renderAt(<TopicsPanel conversationId={CONV} />)
  await userEvent.click(await screen.findByRole('button', { name: /Pedido 837/ }))
}

const ref = (over: Partial<TopicAccountRef> = {}): TopicAccountRef => ({ account_id: 'a1', name: 'ACME Telecom', source: 'topic_link', persisted: true, linked_to_contact: true, ...over })

describe('AccountSection — the company of a subject', () => {
  it('says so when there is no company yet', async () => {
    await open()
    expect(await screen.findByText('Nenhuma empresa definida para este assunto.')).toBeInTheDocument()
  })

  it('a contact with several companies is ASKED, never guessed, and the click records the choice', async () => {
    context = {
      status: 'needs_choice',
      related: [],
      candidates: [
        { account_id: 'a1', name: 'ACME Telecom', relationship_type: 'employee', contact_primary: true },
        { account_id: 'a2', name: 'XPTO Redes', relationship_type: 'owner', contact_primary: false },
      ],
    }
    vi.mocked(axios.post).mockResolvedValue({ data: { status: 'resolved', primary: ref({ account_id: 'a2', name: 'XPTO Redes' }), related: [], candidates: [] } })
    const user = userEvent.setup()
    await open()
    const group = await screen.findByRole('group', { name: 'Escolher a empresa do assunto' })
    expect(within(group).getByText(/pertence a 2 empresas/)).toBeInTheDocument()
    expect(screen.queryByText('principal')).not.toBeInTheDocument() // nothing is pre-selected, not even the contact's own primary
    await user.click(within(group).getByRole('button', { name: 'XPTO Redes' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(String(vi.mocked(axios.post).mock.calls[0][0])).toMatch(new RegExp(`/topics/${T1}/accounts$`))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ account_id: 'a2', relation: 'primary' })
    expect(await screen.findByText('XPTO Redes')).toBeInTheDocument()
    expect(screen.queryByRole('group', { name: 'Escolher a empresa do assunto' })).not.toBeInTheDocument()
  })

  it('shows a derived company with where it came from and lets a person confirm (persist) it', async () => {
    context = { status: 'resolved', primary: ref({ source: 'sole_company', persisted: false }), related: [], candidates: [] }
    vi.mocked(axios.post).mockResolvedValue({ data: { status: 'resolved', primary: ref(), related: [], candidates: [] } })
    const user = userEvent.setup()
    await open()
    expect(await screen.findByText('ACME Telecom')).toBeInTheDocument()
    expect(screen.getByText('única empresa do contato')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Confirmar esta empresa' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ account_id: 'a1', relation: 'primary' })
    expect(await screen.findByText('escolhida')).toBeInTheDocument()
  })

  it('flags a company that is not one of the contact\'s companies, and removes a chosen one', async () => {
    context = { status: 'resolved', primary: ref({ linked_to_contact: false }), related: [ref({ account_id: 'a3', name: 'Zeta' })], candidates: [] }
    vi.mocked(axios.delete).mockResolvedValue({ data: { status: 'none', related: [], candidates: [] } })
    const user = userEvent.setup()
    await open()
    expect(await screen.findByText('fora das empresas do contato')).toBeInTheDocument()
    const related = screen.getByRole('list', { name: 'Empresas relacionadas' })
    expect(within(related).getByText('Zeta')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Remover ACME Telecom' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalled())
    expect(String(vi.mocked(axios.delete).mock.calls[0][0])).toMatch(new RegExp(`/topics/${T1}/accounts/a1$`))
    expect(await screen.findByText('Nenhuma empresa definida para este assunto.')).toBeInTheDocument()
  })

  it('without topic.manage a person only sees the state; nothing can be changed', async () => {
    access = ['topic.read', 'account.read']
    context = {
      status: 'needs_choice',
      related: [],
      candidates: [{ account_id: 'a1', name: 'ACME Telecom', relationship_type: 'employee', contact_primary: true }, { account_id: 'a2', name: 'XPTO Redes', relationship_type: 'owner', contact_primary: false }],
    }
    await open()
    const group = await screen.findByRole('group', { name: 'Escolher a empresa do assunto' })
    expect(within(group).getByRole('button', { name: 'ACME Telecom' })).toBeDisabled()
    expect(within(group).getByText('Quem atende a conversa escolhe a empresa.')).toBeInTheDocument()
  })

  it('does not exist without account.read, or when switched off or forbidden', async () => {
    access = ['topic.read', 'topic.manage']
    await open()
    await screen.findByText(/Detalhes do assunto|Resumo|Chamados do assunto/, undefined, { timeout: 2000 }).catch(() => undefined)
    expect(screen.queryByLabelText('Empresa do assunto')).not.toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => String(u).endsWith('/account-context'))).toBe(false)
  })

  it('a refused change says so and keeps the state', async () => {
    context = {
      status: 'needs_choice',
      related: [],
      candidates: [{ account_id: 'a1', name: 'ACME Telecom', relationship_type: 'employee', contact_primary: true }, { account_id: 'a2', name: 'XPTO Redes', relationship_type: 'owner', contact_primary: false }],
    }
    vi.mocked(axios.post).mockImplementation(() => err(403))
    const user = userEvent.setup()
    await open()
    await user.click(await screen.findByRole('button', { name: 'ACME Telecom' }))
    expect(await screen.findByRole('alert')).toHaveTextContent('Você não tem permissão para esta ação.')
    expect(screen.getByRole('group', { name: 'Escolher a empresa do assunto' })).toBeInTheDocument()
  })
})
