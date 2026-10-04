import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import GroupsPage from '../pages/GroupsPage'
import { authorLabel, groupPreview, messageText, sortGroupMessages } from '../lib/groupModel'
import type { AvailableGroup, Group, GroupMessage } from '../lib/groups'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const READ = ['group.read']
const MANAGE = ['group.read', 'group.manage']

const group = (over: Partial<Group> = {}): Group => ({
  id: 'g-1',
  name: '0 - K3G Solutions Oficial',
  enabled: true,
  last_message_at: new Date().toISOString(),
  last_message: { author_name: 'Rafael', from_me: false, message_type: 'text', preview: 'certo' },
  ...over,
})
const msg = (id: string, minAgo: number, over: Partial<GroupMessage> = {}): GroupMessage => ({
  id,
  author_name: 'Rafael',
  from_me: false,
  message_type: 'text',
  body: `texto ${id}`,
  sent_at: new Date(Date.now() - minAgo * 60_000).toISOString(),
  ...over,
})

interface Server {
  permissions: string[]
  groups?: Group[]
  messages?: GroupMessage[] // newest-first thread, served in pages of `pageSize`
  pageSize?: number
  available?: AvailableGroup[]
  availableTotal?: number
}

function serve(s: Server) {
  const pageSize = s.pageSize ?? 100
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: s.permissions } }
    if (url.endsWith('/groups/available')) {
      const items = (s.available ?? []).filter((g) => !config?.params?.q || g.name.toLowerCase().includes(String(config.params.q).toLowerCase()))
      return { data: { items, count: items.length, total: s.availableTotal ?? items.length } }
    }
    if (/\/groups\/[^/]+\/messages$/.test(url)) {
      const all = [...(s.messages ?? [])].sort((a, b) => b.sent_at.localeCompare(a.sent_at))
      const offset = Number(config?.params?.cursor || 0)
      const items = all.slice(offset, offset + pageSize)
      const more = offset + pageSize < all.length
      return { data: { items, has_more: more, next_cursor: more ? String(offset + pageSize) : undefined } }
    }
    if (url.endsWith('/groups')) return { data: { items: s.groups ?? [] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('groupModel', () => {
  it('shows the text, or the kind of media when there is none, and never invents content', () => {
    expect(messageText({ body: ' oi ', message_type: 'text' })).toBe('oi')
    expect(messageText({ body: '', message_type: 'image' })).toBe('[Foto]')
    expect(messageText({ body: '', message_type: 'audio' })).toBe('[Áudio]')
    expect(messageText({ body: '', message_type: 'text' })).toBe('')
  })
  it('labels the author: "Você" for our own, a neutral word when the name is unknown', () => {
    expect(authorLabel({ author_name: 'Rafael', from_me: false })).toBe('Rafael')
    expect(authorLabel({ author_name: '', from_me: false })).toBe('Participante')
    expect(authorLabel({ author_name: 'Rafael', from_me: true })).toBe('Você')
  })
  it('builds the list preview as "Autor: texto"', () => {
    expect(groupPreview(group())).toBe('Rafael: certo')
    expect(groupPreview(group({ last_message: { author_name: '', from_me: true, message_type: 'image', preview: '' } }))).toBe('Você: [Foto]')
    expect(groupPreview(group({ last_message: null }))).toBe('Sem mensagens')
  })
  it('orders oldest first without duplicates', () => {
    const out = sortGroupMessages([msg('c', 1), msg('a', 30), msg('b', 10), msg('b', 10)])
    expect(out.map((m) => m.id)).toEqual(['a', 'b', 'c'])
  })
})

describe('GroupsPage', () => {
  it('refuses the page without group.read and never asks for groups', async () => {
    serve({ permissions: [] })
    renderAt(<GroupsPage />)
    expect(await screen.findByText(/não tem permissão para ver os grupos/i)).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([u]) => (u as string).endsWith('/groups'))).toBe(false)
  })

  it('tells an administrator how to start, and a reader that an administrator chooses', async () => {
    serve({ permissions: MANAGE, groups: [] })
    const { unmount } = renderAt(<GroupsPage />)
    expect(await screen.findByText('Nenhum grupo habilitado')).toBeInTheDocument()
    expect(await screen.findByRole('button', { name: 'Gerenciar grupos' })).toBeInTheDocument()
    expect(await screen.findByText(/Escolha em "Gerenciar grupos"/)).toBeInTheDocument()
    expect(screen.queryByText(/Um administrador escolhe/)).not.toBeInTheDocument()
    unmount()

    serve({ permissions: READ, groups: [] })
    renderAt(<GroupsPage />)
    expect(await screen.findByText(/Um administrador escolhe/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Gerenciar grupos' })).not.toBeInTheDocument()
  })

  it('opens a group read-only: oldest first, author on each run, own messages as Você, media named, no composer', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      messages: [
        msg('m3', 1, { author_name: '', from_me: true, body: 'ok' }),
        msg('m2', 5, { author_name: 'Felipe', body: '', message_type: 'image' }),
        msg('m1', 10, { author_name: 'Rafael', body: 'primeira' }),
      ],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('primeira')
    const texts = ['primeira', '[Foto]', 'ok'].map((t) => screen.getByText(t))
    // document order = oldest -> newest
    expect(texts[0].compareDocumentPosition(texts[1]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(texts[1].compareDocumentPosition(texts[2]) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy()
    expect(screen.getAllByText('Felipe').length).toBeGreaterThan(0)
    expect(screen.getByText(/Somente leitura/)).toBeInTheDocument()
    expect(screen.getByText(/não são enviadas pelo OMNIRA/)).toBeInTheDocument()
    expect(screen.queryByRole('textbox')).not.toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Apagar histórico' })).not.toBeInTheDocument() // reader only
  })

  it('loads older messages above the ones shown, in order', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ,
      groups: [group()],
      pageSize: 2,
      messages: [msg('m1', 40), msg('m2', 30), msg('m3', 20), msg('m4', 10)],
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('texto m4')
    expect(screen.queryByText('texto m1')).not.toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Carregar mensagens anteriores' }))
    await screen.findByText('texto m1')
    const order = screen.getAllByText(/^texto m/).map((n) => n.textContent)
    expect(order).toEqual(['texto m1', 'texto m2', 'texto m3', 'texto m4'])
  })

  it('lets an administrator delete a group\'s history only after confirming', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group()], messages: [msg('m1', 5)] })
    vi.mocked(axios.delete).mockResolvedValue({ data: { deleted: 1 } })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /0 - K3G Solutions Oficial/ }))
    await screen.findByText('texto m1')
    await user.click(screen.getByRole('button', { name: 'Apagar histórico' }))
    await screen.findByText('Apagar o histórico deste grupo?')
    await user.click(screen.getByRole('button', { name: 'Cancelar' }))
    expect(axios.delete).not.toHaveBeenCalled()
    await user.click(screen.getByRole('button', { name: 'Apagar histórico' }))
    const dialog = await screen.findByText('Apagar o histórico deste grupo?')
    await user.click(within(dialog.closest('[role="dialog"]') as HTMLElement).getByRole('button', { name: 'Apagar histórico' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/groups\/g-1\/messages$/)
  })
})

describe('ManageGroupsModal (via the page)', () => {
  const avail = (over: Partial<AvailableGroup> = {}): AvailableGroup => ({
    provider_group_id: '120363000000000001@g.us',
    name: 'Cobrança',
    participant_count: 5,
    enabled: false,
    group_id: null,
    ...over,
  })

  it('enables a group that OMNIRA does not know yet by its WhatsApp id', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [], available: [avail()] })
    vi.mocked(axios.post).mockResolvedValue({ data: group({ id: 'new', name: 'Cobrança' }) })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    await user.click(await screen.findByRole('button', { name: 'Ativar Cobrança' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ provider_group_id: '120363000000000001@g.us' })
  })

  it('turns a known group off through its id, keeping the history', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [group()], available: [avail({ enabled: true, group_id: 'g-1', name: 'Oficial' })] })
    vi.mocked(axios.patch).mockResolvedValue({ data: group({ enabled: false }) })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText('Lendo')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: 'Desativar Oficial' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/groups\/g-1$/)
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ enabled: false })
  })

  it('searches by name on the server and says when there are more than shown', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [], available: [avail(), avail({ provider_group_id: '120363000000000002@g.us', name: 'Amigos' })], availableTotal: 624 })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText(/Mostrando 2 de 624/)).toBeInTheDocument()
    await user.type(screen.getByLabelText('Buscar grupo'), 'cobr')
    await waitFor(() => expect(screen.queryByText('Amigos')).not.toBeInTheDocument())
    expect(await screen.findByText('Cobrança')).toBeInTheDocument()
    expect(vi.mocked(axios.get).mock.calls.some(([, c]) => (c as any)?.params?.q === 'cobr')).toBe(true)
  })

  it('explains a missing WhatsApp connection instead of failing silently', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE, groups: [] })
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: MANAGE } }
      if (url.endsWith('/groups/available')) return Promise.reject({ response: { status: 409 } })
      return { data: { items: [] } }
    })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: 'Gerenciar grupos' }))
    expect(await screen.findByText(/Não há uma conexão ativa do WhatsApp/)).toBeInTheDocument()
  })
})
