import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import PeopleAndGroupsPage from '../pages/PeopleAndGroupsPage'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const ADMIN = ['agent.read', 'agent.manage', 'membership.read']
const READ_ONLY = ['agent.read', 'membership.read']

type Member = { membership_id: string; user_id: string; name: string; email: string; role_key: string; role_name: string; status: string; created_at: string }
const member = (id: string, name: string, over: Partial<Member> = {}): Member => ({
  membership_id: id, user_id: 'u-' + id, name, email: `${name.split(' ')[0].toLowerCase()}@k3g.com`,
  role_key: 'tenant_agent', role_name: 'Agente', status: 'active', created_at: '2026-01-01T00:00:00Z', ...over,
})
const queue = (id: string, name: string, over: object = {}) => ({
  id, name, mode: 'manual', is_default: false, member_count: 0, available_count: 0, open_conversation_count: 0,
  created_at: '', updated_at: '', ...over,
})

interface World {
  permissions: string[]
  team: Member[]
  agents: any[]
  queues: any[]
  failNext?: { status: number; data: string } | null
  teamFails?: boolean
}

// A tiny stateful API: reads answer from `world`, writes record the call and change it,
// so what the screen shows after a refetch is what the "server" now holds.
function serve(world: World) {
  const calls: { method: string; url: string; body?: any }[] = []
  const answer = (data: any) => Promise.resolve({ data })
  const maybeFail = () => {
    if (world.failNext) {
      const f = world.failNext
      world.failNext = null
      return Promise.reject({ response: { status: f.status, data: f.data } })
    }
    return null
  }
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return answer({ role_key: 'tenant_admin', permissions: world.permissions, invitation_delivery_available: true })
    if (url.endsWith('/team')) return world.teamFails ? Promise.reject({ response: { status: 500 } }) : answer({ items: world.team })
    if (url.endsWith('/agents')) return answer({ items: world.agents })
    if (url.endsWith('/queues')) return answer({ items: world.queues, count: world.queues.length })
    return Promise.reject({ response: { status: 404 } })
  })
  vi.mocked(axios.post).mockImplementation(async (url: string, body: any) => {
    calls.push({ method: 'POST', url, body })
    const fail = maybeFail(); if (fail) return fail
    if (url.endsWith('/agents')) {
      const m = world.team.find((x) => x.membership_id === body.membership_id)!
      world.agents.push({ id: 'p-' + m.membership_id, membership_id: m.membership_id, user_id: m.user_id, name: m.name, email: m.email, role: m.role_name, status: 'active', queues: [] })
      return answer({ id: 'p-' + m.membership_id, status: 'active' })
    }
    if (/\/agents\/[^/]+\/queues$/.test(url)) {
      const profile = world.agents.find((a) => url.includes('/agents/' + a.id + '/'))!
      const q = world.queues.find((x) => x.id === body.queue_id)!
      profile.queues.push({ id: 'qm-' + q.id, queue_id: q.id, queue_name: q.name, available: body.available, capacity: body.capacity })
      return answer({ id: 'qm-' + q.id })
    }
    if (url.endsWith('/queues')) {
      if (body.is_default) world.queues.forEach((q) => (q.is_default = false))
      world.queues.push(queue('q-new', body.name, { mode: body.mode, is_default: body.is_default }))
      return answer({ id: 'q-new' })
    }
    return answer({})
  })
  vi.mocked(axios.patch).mockImplementation(async (url: string, body: any) => {
    calls.push({ method: 'PATCH', url, body })
    const fail = maybeFail(); if (fail) return fail
    const agent = world.agents.find((a) => url.endsWith('/agents/' + a.id))
    if (agent) agent.status = body.status
    const qm = url.match(/\/agents\/([^/]+)\/queues\/([^/]+)$/)
    if (qm) {
      const a = world.agents.find((x) => x.id === qm[1])!
      const row = a.queues.find((x: any) => x.id === qm[2])
      row.available = body.available; row.capacity = body.capacity
    }
    const q = world.queues.find((x) => url.endsWith('/queues/' + x.id))
    if (q) {
      if (body.is_default) world.queues.forEach((x) => (x.is_default = false))
      Object.assign(q, body)
    }
    return answer({})
  })
  vi.mocked(axios.delete).mockImplementation(async (url: string) => {
    calls.push({ method: 'DELETE', url })
    const fail = maybeFail(); if (fail) return fail
    const qm = url.match(/\/agents\/([^/]+)\/queues\/([^/]+)$/)
    if (qm) {
      const a = world.agents.find((x) => x.id === qm[1])!
      a.queues = a.queues.filter((x: any) => x.id !== qm[2])
    } else {
      world.queues = world.queues.filter((x) => !url.endsWith('/queues/' + x.id))
    }
    return answer(undefined)
  })
  return calls
}

function baseWorld(over: Partial<World> = {}): World {
  return {
    permissions: ADMIN,
    team: [member('m1', 'Ana Souza'), member('m2', 'Bruno Lima', { role_key: 'tenant_supervisor', role_name: 'Supervisor' }), member('m3', 'Carla Dias')],
    agents: [
      { id: 'p1', membership_id: 'm1', user_id: 'u-m1', name: 'Ana Souza', email: 'ana@k3g.com', role: 'Agente', status: 'active',
        queues: [{ id: 'qm1', queue_id: 'q1', queue_name: 'Suporte', available: true, capacity: 3 }, { id: 'qm2', queue_id: 'q2', queue_name: 'Financeiro', available: false, capacity: 1 }] },
      { id: 'p2', membership_id: 'm2', user_id: 'u-m2', name: 'Bruno Lima', email: 'bruno@k3g.com', role: 'Supervisor', status: 'disabled', queues: [] },
    ],
    queues: [
      queue('q1', 'Suporte', { mode: 'round_robin', is_default: true, member_count: 1, available_count: 1, open_conversation_count: 4 }),
      queue('q2', 'Financeiro', { member_count: 1, open_conversation_count: 1 }),
      queue('q3', 'Comercial'),
    ],
    ...over,
  }
}

const open = (path = '/settings/people') => renderAt(<PeopleAndGroupsPage />, path, '/settings/people')
// The table and the stacked cards are both in the DOM (CSS hides one); work on the table,
// once it has loaded (a skeleton is shown until then).
const table = () => screen.getByRole('table')
const row = (name: string) => within(table()).getByText(name).closest('tr')!
const tableLoaded = async () => within(await screen.findByRole('table'))

describe('PeopleAndGroupsPage: Pessoas', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    setSession()
  })

  it('shows who is an operator, who is paused and who is not, with the groups they attend', async () => {
    serve(baseWorld())
    open()

    await screen.findByRole('heading', { name: 'Pessoas e grupos' })
    await (await tableLoaded()).findByText('Ana Souza')
    const ana = row('Ana Souza')
    expect(within(ana).getByText('Operador')).toBeInTheDocument()
    expect(within(ana).getByText('Suporte')).toBeInTheDocument()
    expect(within(ana).getByText('Financeiro · indisponível')).toBeInTheDocument()
    expect(within(row('Bruno Lima')).getByText('Pausado')).toBeInTheDocument()
    expect(within(row('Carla Dias')).getByRole('button', { name: 'Tornar operador' })).toBeInTheDocument()
    expect(within(row('Carla Dias')).getByText('Torne operador para atribuir grupos')).toBeInTheDocument()
  })

  it('does not list people whose membership is not active', async () => {
    serve(baseWorld({ team: [member('m1', 'Ana Souza'), member('m9', 'Revogado Silva', { status: 'revoked' })] }))
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    expect(screen.queryByText('Revogado Silva')).not.toBeInTheDocument()
  })

  it('makes someone an operator through POST /agents with their membership', async () => {
    const world = baseWorld()
    const calls = serve(world)
    open()
    await userEvent.click(await (await tableLoaded()).findByRole('button', { name: 'Tornar operador' }))

    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'POST', body: { membership_id: 'm3' } })))
    // After the refetch the person now reads as operator.
    await waitFor(() => expect(within(row('Carla Dias')).getByText('Operador')).toBeInTheDocument())
  })

  it('pauses and reactivates an operator through PATCH status', async () => {
    const world = baseWorld()
    const calls = serve(world)
    open()
    await (await tableLoaded()).findByText('Ana Souza')

    await userEvent.click(within(row('Ana Souza')).getByRole('button', { name: 'Pausar' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { status: 'disabled' } })))
    await waitFor(() => expect(within(row('Ana Souza')).getByText('Pausado')).toBeInTheDocument())

    await userEvent.click(within(row('Ana Souza')).getByRole('button', { name: 'Reativar' }))
    await waitFor(() => expect(within(row('Ana Souza')).getByText('Operador')).toBeInTheDocument())
  })

  // The API only adds ACTIVE operators to a group, so the control is off and says why.
  it('keeps group management off for a paused operator and explains it', async () => {
    serve(baseWorld())
    open()
    await (await tableLoaded()).findByText('Bruno Lima')
    expect(within(row('Bruno Lima')).getByRole('button', { name: 'Gerenciar grupos' })).toBeDisabled()
    expect(within(row('Bruno Lima')).getByText('Reative para gerenciar grupos')).toBeInTheDocument()
  })

  it('manages a person\'s groups in a dialog: join, leave, availability and capacity', async () => {
    const world = baseWorld()
    const calls = serve(world)
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    await userEvent.click(within(row('Ana Souza')).getByRole('button', { name: 'Gerenciar grupos' }))

    const dialog = await screen.findByRole('dialog', { name: 'Grupos de Ana Souza' })
    // Every group is listed; only the ones she attends are checked.
    expect(within(dialog).getByRole('checkbox', { name: 'Participa em Suporte' })).toBeChecked()
    expect(within(dialog).getByRole('checkbox', { name: 'Participa em Comercial' })).not.toBeChecked()

    // Join: capacity 1 and available by default.
    await userEvent.click(within(dialog).getByRole('checkbox', { name: 'Participa em Comercial' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'POST', body: { queue_id: 'q3', available: true, capacity: 1 } })))
    await waitFor(() => expect(within(dialog).getByRole('checkbox', { name: 'Participa em Comercial' })).toBeChecked())

    // Availability is saved the moment it flips, keeping the saved capacity.
    await userEvent.click(within(dialog).getByRole('switch', { name: 'Disponível em Suporte' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { available: false, capacity: 3 } })))

    // Capacity is typed, then saved with the button; the button is idle until it changes.
    const save = within(dialog).getByRole('button', { name: 'Salvar capacidade em Suporte' })
    expect(save).toBeDisabled()
    const capacity = within(dialog).getByRole('spinbutton', { name: 'Capacidade em Suporte' })
    await userEvent.clear(capacity)
    await userEvent.type(capacity, '7')
    expect(save).toBeEnabled()
    await userEvent.click(save)
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { available: false, capacity: 7 } })))

    // Leave a group.
    await userEvent.click(within(dialog).getByRole('checkbox', { name: 'Participa em Financeiro' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'DELETE', url: expect.stringMatching(/\/agents\/p1\/queues\/qm2$/) })))
  })

  // Clearing the field to type a new number must not snap to 1 while typing ("7" must not become "17").
  it('lets you type a capacity freely and clamps it to 1..100 when you leave the field or save', async () => {
    const calls = serve(baseWorld())
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    await userEvent.click(within(row('Ana Souza')).getByRole('button', { name: 'Gerenciar grupos' }))
    const dialog = await screen.findByRole('dialog')
    const capacity = within(dialog).getByRole('spinbutton', { name: 'Capacidade em Suporte' })

    await userEvent.clear(capacity)
    expect(capacity).toHaveValue(null) // empty while editing, not forced to 1
    await userEvent.type(capacity, '7')
    expect(capacity).toHaveValue(7)

    await userEvent.clear(capacity)
    await userEvent.type(capacity, '500')
    await userEvent.tab()
    expect(capacity).toHaveValue(100)

    await userEvent.clear(capacity)
    await userEvent.type(capacity, '0')
    await userEvent.tab()
    expect(capacity).toHaveValue(1)

    // Saving without leaving the field first also sends the clamped value.
    await userEvent.clear(capacity)
    await userEvent.type(capacity, '250')
    await userEvent.click(within(dialog).getByRole('button', { name: 'Salvar capacidade em Suporte' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { available: true, capacity: 100 } })))
  })

  it('closes the dialog with Esc', async () => {
    serve(baseWorld())
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    await userEvent.click(within(row('Ana Souza')).getByRole('button', { name: 'Gerenciar grupos' }))
    await screen.findByRole('dialog')
    await userEvent.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })
})

describe('PeopleAndGroupsPage: Grupos', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    setSession()
  })

  const openGroups = () => open('/settings/people?aba=grupos')
  const card = (name: string) => screen.getByRole('heading', { name }).closest('li')!

  it('lists the groups with the real counts, the default and the mode', async () => {
    serve(baseWorld())
    openGroups()
    await screen.findByRole('heading', { name: 'Suporte' })

    const suporte = card('Suporte')
    expect(within(suporte).getByText('Padrão')).toBeInTheDocument()
    // "Rodízio" is both the badge and an option of the mode selector.
    expect(within(suporte).getAllByText('Rodízio').length).toBeGreaterThanOrEqual(1)
    expect(within(suporte).getByLabelText('Modo de Suporte')).toHaveValue('round_robin')
    expect(within(suporte).getByText(/1 pessoa · 1 disponível · 4 conversas abertas/)).toBeInTheDocument()
    expect(within(card('Comercial')).getByText(/0 pessoas · 0 disponíveis · 0 conversas abertas/)).toBeInTheDocument()
    expect(within(card('Financeiro')).getByText(/1 conversa aberta/)).toBeInTheDocument()
  })

  it('creates a group and can make it the default at the same time', async () => {
    const calls = serve(baseWorld())
    openGroups()
    await userEvent.click(await screen.findByRole('button', { name: 'Novo grupo' }))

    const dialog = await screen.findByRole('dialog', { name: 'Novo grupo' })
    const create = within(dialog).getByRole('button', { name: 'Criar grupo' })
    expect(create).toBeDisabled() // a name is required
    await userEvent.type(within(dialog).getByLabelText('Nome'), '  Suporte N2  ')
    await userEvent.click(within(dialog).getByRole('radio', { name: 'Rodízio' }))
    await userEvent.click(within(dialog).getByRole('checkbox', { name: 'Usar como grupo padrão' }))
    await userEvent.click(create)

    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'POST', body: { name: 'Suporte N2', mode: 'round_robin', is_default: true } })))
    await waitFor(() => expect(within(card('Suporte N2')).getByText('Padrão')).toBeInTheDocument())
    // The previous default stopped being the default.
    expect(within(card('Suporte')).queryByText('Padrão')).not.toBeInTheDocument()
  })

  it('renames, changes the mode and makes a group the default', async () => {
    const calls = serve(baseWorld())
    openGroups()
    await screen.findByRole('heading', { name: 'Comercial' })

    await userEvent.click(within(card('Comercial')).getByRole('button', { name: 'Renomear' }))
    // While renaming, the title is replaced by the edit field, so look it up directly.
    const input = screen.getByLabelText('Novo nome de Comercial')
    await userEvent.clear(input)
    await userEvent.type(input, 'Vendas{enter}')
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { name: 'Vendas' } })))
    await screen.findByRole('heading', { name: 'Vendas' })

    await userEvent.selectOptions(within(card('Vendas')).getByLabelText('Modo de Vendas'), 'round_robin')
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { mode: 'round_robin' } })))

    await userEvent.click(within(card('Vendas')).getByRole('button', { name: 'Definir como padrão' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'PATCH', body: { is_default: true } })))
    await waitFor(() => expect(within(card('Vendas')).getByText('Padrão')).toBeInTheDocument())
  })

  it('asks before deleting and deletes only after confirmation', async () => {
    const calls = serve(baseWorld())
    openGroups()
    await screen.findByRole('heading', { name: 'Comercial' })

    await userEvent.click(within(card('Comercial')).getByRole('button', { name: 'Excluir' }))
    const dialog = await screen.findByRole('dialog', { name: 'Excluir o grupo Comercial?' })
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)
    await userEvent.click(within(dialog).getByRole('button', { name: 'Cancelar' }))
    expect(calls.filter((c) => c.method === 'DELETE')).toHaveLength(0)

    await userEvent.click(within(card('Comercial')).getByRole('button', { name: 'Excluir' }))
    await userEvent.click(within(await screen.findByRole('dialog')).getByRole('button', { name: 'Excluir' }))
    await waitFor(() => expect(calls).toContainEqual(expect.objectContaining({ method: 'DELETE', url: expect.stringMatching(/\/queues\/q3$/) })))
    await waitFor(() => expect(screen.queryByRole('heading', { name: 'Comercial' })).not.toBeInTheDocument())
  })

  it('does not offer to delete the default group and says why', async () => {
    serve(baseWorld())
    openGroups()
    await screen.findByRole('heading', { name: 'Suporte' })
    expect(within(card('Suporte')).getByRole('button', { name: 'Excluir' })).toBeDisabled()
    expect(within(card('Suporte')).getByText('Defina outro grupo como padrão antes de excluir')).toBeInTheDocument()
    expect(within(card('Suporte')).queryByRole('button', { name: 'Definir como padrão' })).not.toBeInTheDocument()
  })

  // Without a default group new conversations are not distributed at all.
  it('warns, from either tab, when no group is the default, and jumps to the groups tab', async () => {
    serve(baseWorld({ queues: [queue('q1', 'Suporte'), queue('q2', 'Financeiro')] }))
    open()

    const banner = await screen.findByRole('status')
    expect(banner).toHaveTextContent('Nenhum grupo é o padrão: as conversas novas não estão sendo distribuídas.')
    await userEvent.click(within(banner).getByRole('button', { name: 'Ir para Grupos' }))
    await screen.findByRole('heading', { name: 'Suporte' })
    expect(screen.getByRole('tab', { name: 'Grupos' })).toHaveAttribute('aria-selected', 'true')
  })

  it('shows no banner when a default exists', async () => {
    serve(baseWorld())
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    expect(screen.queryByRole('status')).not.toBeInTheDocument()
  })

  it('invites the first group when there are none', async () => {
    serve(baseWorld({ queues: [] }))
    openGroups()
    expect(await screen.findByText('Nenhum grupo ainda. Crie o primeiro para começar a distribuir conversas.')).toBeInTheDocument()
  })

  it('turns an API refusal into a sentence and still refreshes the screen', async () => {
    const world = baseWorld()
    serve(world)
    openGroups()
    await screen.findByRole('heading', { name: 'Financeiro' })

    world.failNext = { status: 409, data: 'a queue with this name already exists' }
    await userEvent.click(within(card('Financeiro')).getByRole('button', { name: 'Renomear' }))
    const input = screen.getByLabelText('Novo nome de Financeiro')
    await userEvent.clear(input)
    await userEvent.type(input, 'Suporte{enter}')

    expect(await screen.findByRole('alert')).toHaveTextContent('Já existe um grupo com esse nome.')
    // The raw API text never reaches the screen.
    expect(screen.queryByText(/a queue with this name already exists/)).not.toBeInTheDocument()
    // The next successful action clears the message.
    await userEvent.selectOptions(within(card('Financeiro')).getByLabelText('Modo de Financeiro'), 'round_robin')
    await waitFor(() => expect(screen.queryByRole('alert')).not.toBeInTheDocument())
  })

  it('moves between tabs and keeps the choice in the address', async () => {
    serve(baseWorld())
    open()
    await (await tableLoaded()).findByText('Ana Souza')
    await userEvent.click(screen.getByRole('tab', { name: 'Grupos' }))
    expect(await screen.findByRole('button', { name: 'Novo grupo' })).toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Pessoas' }))
    expect(await screen.findByRole('table')).toBeInTheDocument()
  })
})

describe('PeopleAndGroupsPage: permissões e estados', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    localStorage.clear()
    setSession()
  })

  it('lets someone with agent.read look but not change anything', async () => {
    serve(baseWorld({ permissions: READ_ONLY }))
    open()
    await (await tableLoaded()).findByText('Ana Souza')

    expect(screen.getByText('Você pode ver, mas não alterar.')).toBeInTheDocument()
    for (const label of ['Tornar operador', 'Pausar', 'Reativar', 'Gerenciar grupos']) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument()
    }
    await userEvent.click(screen.getByRole('tab', { name: 'Grupos' }))
    await screen.findByRole('heading', { name: 'Suporte' })
    for (const label of ['Novo grupo', 'Renomear', 'Excluir', 'Definir como padrão']) {
      expect(screen.queryByRole('button', { name: label })).not.toBeInTheDocument()
    }
    expect(screen.queryByLabelText('Modo de Suporte')).not.toBeInTheDocument()
  })

  it('shows an access-restricted message and loads nothing without agent.read', async () => {
    serve(baseWorld({ permissions: [] }))
    open()

    expect(await screen.findByText('Você não tem permissão para ver pessoas e grupos.')).toBeInTheDocument()
    const reads = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]))
    expect(reads.some((u) => /\/(team|agents|queues)$/.test(u))).toBe(false)
  })

  it('reports a failure to load the people without breaking the groups tab', async () => {
    serve(baseWorld({ teamFails: true }))
    open()
    expect(await screen.findByText('Não foi possível carregar as pessoas.')).toBeInTheDocument()
    await userEvent.click(screen.getByRole('tab', { name: 'Grupos' }))
    expect(await screen.findByRole('heading', { name: 'Suporte' })).toBeInTheDocument()
  })
})
