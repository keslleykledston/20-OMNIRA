import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import PeoplePage from '../pages/PeoplePage'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const ADMIN = ['membership.read', 'membership.manage', 'agent.read', 'agent.manage']
const SUPERVISOR = ['membership.read', 'agent.read', 'agent.manage']

interface World {
  permissions: string[]
  delivery: boolean
  members: any[]
  invitations: any[]
  agents: any[]
  queues: any[]
  sectors: any[]
  online: string[]
  shifts: any[]
  failNext?: { status: number; data?: string } | null
}
type Call = { method: string; url: string; body?: any }

const member = (id: string, name: string, over: any = {}) => ({
  membership_id: 'm-' + id, user_id: 'u-' + id, name, email: `${id}@k3g.com`, role_key: 'tenant_agent', role_name: 'Tenant Agent',
  status: 'active', created_at: '2026-01-01T00:00:00Z', last_login_at: '2026-10-01T12:00:00Z', ...over,
})
const agent = (id: string, queues: any[] = [], over: any = {}) => ({
  id: 'p-' + id, membership_id: 'm-' + id, user_id: 'u-' + id, name: id, email: `${id}@k3g.com`, role: 'Agente', status: 'active', queues, ...over,
})
const seat = (memberId: string, queueId: string, queueName: string, over: any = {}) => ({ id: memberId, queue_id: queueId, queue_name: queueName, available: true, capacity: 2, ...over })
const queue = (id: string, name: string, over: any = {}) => ({ id, name, mode: 'manual', is_default: false, member_count: 0, available_count: 0, open_conversation_count: 0, created_at: '', updated_at: '', ...over })
const invitation = (id: string, email: string, over: any = {}) => ({
  id, email, role_key: 'tenant_agent', role_name: 'Tenant Agent', status: 'pending', created_by_email: 'admin@k3g.com',
  expires_at: '2099-01-01T00:00:00Z', created_at: '2026-10-01T00:00:00Z', sent_at: '2026-10-01T00:01:00Z', ...over,
})
const ROLES = [
  { id: 'r1', key: 'tenant_admin', name: 'Tenant Administrator', permissions: [] },
  { id: 'r2', key: 'tenant_supervisor', name: 'Tenant Supervisor', permissions: [] },
  { id: 'r3', key: 'tenant_agent', name: 'Tenant Agent', permissions: [] },
]

function world(over: Partial<World> = {}): World {
  return {
    permissions: ADMIN,
    delivery: true,
    members: [
      member('ana', 'Ana Souza', { role_key: 'tenant_admin', role_name: 'Tenant Administrator' }),
      member('bia', 'Bia Lima'),
      member('caio', 'Caio Reis'),
      member('dani', 'Dani Prado', { role_key: 'tenant_supervisor' }),
      member('rui', 'Rui Antigo', { status: 'revoked' }),
    ],
    invitations: [invitation('i1', 'nova@k3g.com'), invitation('i2', 'velha@k3g.com', { status: 'accepted' })],
    agents: [
      agent('ana', [seat('qm-ana', 'q-sup', 'Suporte N1')]),
      agent('bia', []),
      agent('caio', [seat('qm-caio', 'q-fin', 'Cobrança')], { status: 'disabled' }),
    ],
    queues: [queue('q-sup', 'Suporte N1', { is_default: true }), queue('q-fin', 'Cobrança')],
    sectors: [
      { id: 's-sup', name: 'Suporte', active: true, queues: [{ id: 'q-sup', name: 'Suporte N1' }] },
      { id: 's-fin', name: 'Financeiro', active: true, queues: [{ id: 'q-fin', name: 'Cobrança' }] },
    ],
    online: ['p-ana'],
    shifts: [],
    ...over,
  }
}

function serve(w: World) {
  const calls: Call[] = []
  const ok = (data: any) => Promise.resolve({ data })
  const fail = () => {
    if (!w.failNext) return null
    const f = w.failNext
    w.failNext = null
    return Promise.reject({ response: { status: f.status, data: f.data ?? '' } })
  }
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return ok({ role_key: 'tenant_admin', permissions: w.permissions, invitation_delivery_available: w.delivery })
    if (url.endsWith('/team/invitations')) return ok({ items: w.invitations })
    if (url.endsWith('/team')) return ok({ items: w.members })
    if (url.endsWith('/roles')) return ok({ items: ROLES })
    if (url.endsWith('/agents/presence')) return ok({ online_agent_profile_ids: w.online })
    if (url.endsWith('/agents')) return ok({ items: w.agents })
    if (url.endsWith('/queues')) return ok({ items: w.queues })
    if (url.endsWith('/sectors')) return ok({ items: w.sectors })
    if (url.endsWith('/shifts')) return ok({ items: w.shifts })
    if (url.endsWith('/calendars')) return ok({ items: [{ id: 'c1', name: 'Comercial', kind: 'business', timezone: 'America/Manaus', revision: 1, in_use: 1, updated_at: '' }], default_timezone: 'America/Manaus' })
    return Promise.reject({ response: { status: 404, data: 'not found' } })
  })
  for (const method of ['post', 'patch', 'delete'] as const) {
    vi.mocked(axios[method]).mockImplementation(async (url: string, body?: any) => {
      calls.push({ method: method.toUpperCase(), url, body: method === 'delete' ? undefined : body })
      const f = fail()
      if (f) return f
      if (method === 'post' && url.endsWith('/team/invitations')) return ok(invitation('i-new', body.email, { role_key: body.role_key }))
      if (method === 'post' && url.endsWith('/resend')) return ok(invitation('i1', 'nova@k3g.com'))
      if (method === 'post' && url.endsWith('/agents')) {
        const id = String(body.membership_id).replace(/^m-/, '')
        w.agents = [...w.agents, agent(id, [seat('qm-' + id + '-def', 'q-sup', 'Suporte N1', { capacity: 5 })])]
        return ok({ id: 'p-' + id, status: 'active' })
      }
      const queuePath = url.match(/\/agents\/(p-[a-z]+)\/queues(?:\/([^/]+))?$/)
      if (queuePath) {
        const a = w.agents.find((x: any) => x.id === queuePath[1])
        if (a && method === 'post') a.queues = [...a.queues, seat('qm-' + body.queue_id, body.queue_id, body.queue_id, { available: body.available, capacity: body.capacity })]
        if (a && method === 'patch') a.queues = a.queues.map((q: any) => (q.id === queuePath[2] ? { ...q, ...body } : q))
        if (a && method === 'delete') a.queues = a.queues.filter((q: any) => q.id !== queuePath[2])
      }
      const statusPath = method === 'patch' && body?.status && url.match(/\/agents\/(p-[a-z]+)$/)
      if (statusPath) w.agents = w.agents.map((x: any) => (x.id === statusPath[1] ? { ...x, status: body.status } : x))
      const roleChange = method === 'patch' && body?.role_key && url.match(/\/team\/(m-[a-z]+)$/)
      if (roleChange) w.members = w.members.map((m: any) => (m.membership_id === roleChange[1] ? { ...m, role_key: body.role_key } : m))
      return ok({})
    })
  }
  return calls
}

const page = (path = '/settings/people') => renderAt(<PeoplePage />, path, '/settings/people')
const rowOf = (name: string) => screen.findByRole('button', { name: `Abrir a ficha de ${name}` })
const sheet = () => screen.getByRole('dialog', { name: /Ficha de/ })

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setSession()
  vi.stubGlobal('fetch', vi.fn(() => new Promise(() => {})))
})
afterEach(() => {
  vi.unstubAllGlobals()
})

describe('Pessoas: a lista', () => {
  it('mostra membros e convites numa lista só, dizendo quem recebe conversas e o que falta', async () => {
    serve(world())
    page()
    const ana = await rowOf('Ana Souza')
    expect(within(ana).getByText('Recebe conversas')).toBeInTheDocument()
    expect(within(ana).getByText('online')).toBeInTheDocument()
    expect(within(ana).getByText('Suporte N1')).toBeInTheDocument()
    expect(within(await rowOf('Bia Lima')).getByText('Sem fila')).toBeInTheDocument()
    expect(within(await rowOf('Bia Lima')).getByText('Falta escolher ao menos uma fila')).toBeInTheDocument()
    expect(within(await rowOf('Caio Reis')).getByText('Em pausa')).toBeInTheDocument()
    expect(within(await rowOf('Dani Prado')).getByText('Não atende')).toBeInTheDocument()
    expect(within(await rowOf('nova@k3g.com')).getByText('Convite enviado')).toBeInTheDocument()
    // the accepted invitation is the member row itself, and a removed person stays out until asked for
    expect(screen.queryByRole('button', { name: 'Abrir a ficha de velha@k3g.com' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Abrir a ficha de Rui Antigo' })).toBeNull()
  })

  it('o resumo conta cada situação e filtra a lista', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    const chip = await screen.findByRole('button', { name: /Precisam de atenção/ })
    expect(within(chip).getByText('1')).toBeInTheDocument()
    await user.click(chip)
    expect(chip).toHaveAttribute('aria-pressed', 'true')
    expect(await rowOf('Bia Lima')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Abrir a ficha de Ana Souza' })).toBeNull()
  })

  it('busca por nome, e-mail ou fila, e filtra por função e por fila', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await rowOf('Ana Souza')
    await user.type(screen.getByLabelText('Buscar pessoas'), 'suporte')
    expect(screen.getAllByRole('button', { name: /Abrir a ficha/ }).map((b) => b.getAttribute('aria-label'))).toEqual(['Abrir a ficha de Ana Souza'])
    await user.clear(screen.getByLabelText('Buscar pessoas'))
    await user.selectOptions(screen.getByLabelText('Filtrar por função'), 'tenant_supervisor')
    expect(screen.getAllByRole('button', { name: /Abrir a ficha/ }).map((b) => b.getAttribute('aria-label'))).toEqual(['Abrir a ficha de Dani Prado'])
    await user.selectOptions(screen.getByLabelText('Filtrar por função'), '')
    await user.selectOptions(screen.getByLabelText('Filtrar por fila'), 'q-fin')
    expect(screen.getAllByRole('button', { name: /Abrir a ficha/ }).map((b) => b.getAttribute('aria-label'))).toEqual(['Abrir a ficha de Caio Reis'])
  })

  it('mostra removidos e convites cancelados só quando pedido', async () => {
    const user = userEvent.setup()
    serve(world({ invitations: [invitation('i9', 'cancelado@k3g.com', { status: 'revoked' })] }))
    page()
    await user.click(await screen.findByRole('button', { name: 'Mostrar removidos e convites cancelados (2)' }))
    expect(within(await rowOf('Rui Antigo')).getByText('Acesso removido')).toBeInTheDocument()
    expect(within(await rowOf('cancelado@k3g.com')).getByText('Convite cancelado')).toBeInTheDocument()
  })

  it('sem permissão para ver a equipe mostra o acesso restrito e não lê nada', async () => {
    serve(world({ permissions: [] }))
    page()
    expect(await screen.findByText('Você não tem permissão para ver as pessoas desta instância.')).toBeInTheDocument()
    const urls = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]))
    expect(urls.filter((u) => !u.endsWith('/me/access'))).toEqual([])
  })

  it('explica uma falha ao carregar e oferece tentar de novo', async () => {
    serve(world())
    vi.mocked(axios.get).mockImplementation(async (url: string) => {
      if (url.endsWith('/me/access')) return { data: { role_key: 'tenant_admin', permissions: ADMIN, invitation_delivery_available: true } }
      return Promise.reject({ response: { status: 500 } })
    })
    page()
    expect(await screen.findByText('Não foi possível carregar as pessoas.')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Tentar novamente' })).toBeInTheDocument()
  })
})

describe('Pessoas: adicionar pessoa', () => {
  it('convida com a função escolhida e confirma o envio', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    const dialog = screen.getByRole('dialog', { name: 'Adicionar pessoa' })
    expect(within(dialog).getByRole('radio', { name: /Agente/ })).toBeChecked()
    await user.type(within(dialog).getByLabelText('E-mail'), '  Novo.Agente@K3G.com ')
    await user.click(within(dialog).getByRole('radio', { name: /Supervisor/ }))
    await user.click(within(dialog).getByRole('button', { name: 'Enviar convite' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/team/invitations'))?.body).toEqual({ email: 'novo.agente@k3g.com', role_key: 'tenant_supervisor' }))
    expect(await screen.findByText('Convite enviado para novo.agente@k3g.com.')).toBeInTheDocument()
  })

  it('valida o e-mail e não convida quem já está na instância', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    const dialog = screen.getByRole('dialog', { name: 'Adicionar pessoa' })
    await user.type(within(dialog).getByLabelText('E-mail'), 'sem-arroba')
    await user.click(within(dialog).getByRole('button', { name: 'Enviar convite' }))
    expect(within(dialog).getByText('Informe um e-mail válido.')).toBeInTheDocument()
    await user.clear(within(dialog).getByLabelText('E-mail'))
    await user.type(within(dialog).getByLabelText('E-mail'), 'bia@k3g.com')
    expect(within(dialog).getByText('Bia Lima já faz parte desta instância.')).toBeInTheDocument()
    expect(within(dialog).getByRole('button', { name: 'Enviar convite' })).toBeDisabled()
    await user.click(within(dialog).getByRole('button', { name: 'Abrir a ficha' }))
    expect(await screen.findByRole('dialog', { name: 'Ficha de Bia Lima' })).toBeInTheDocument()
    expect(calls.some((c) => c.url.endsWith('/team/invitations'))).toBe(false)
  })

  it('avisa que um convite novo substitui o pendente', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    await user.type(screen.getByLabelText('E-mail'), 'nova@k3g.com')
    expect(screen.getByText('Já existe um convite para este e-mail. Enviar de novo substitui o anterior.')).toBeInTheDocument()
  })

  it('explica uma entrega que falhou (502) apontando o reenvio', async () => {
    const user = userEvent.setup()
    const w = world()
    serve(w)
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    await user.type(screen.getByLabelText('E-mail'), 'outra@k3g.com')
    w.failNext = { status: 502 }
    await user.click(screen.getByRole('button', { name: 'Enviar convite' }))
    expect(await screen.findByText(/o e-mail não pôde ser entregue/)).toBeInTheDocument()
  })

  it('sem envio de convites configurado, o botão fica desligado e diz por quê', async () => {
    serve(world({ delivery: false }))
    page()
    const button = await screen.findByRole('button', { name: /Adicionar pessoa/ })
    expect(button).toBeDisabled()
    expect(button).toHaveAttribute('title', 'O envio de convites ainda não está configurado neste ambiente.')
  })
})

describe('Pessoas: convite com plano de atendimento (ADR-0046)', () => {
  it('um agente convidado já leva as filas e a capacidade, aplicadas no aceite', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    const dialog = screen.getByRole('dialog', { name: 'Adicionar pessoa' })
    await user.type(within(dialog).getByLabelText('E-mail'), 'agente.novo@k3g.com')
    expect(within(dialog).getByRole('switch', { name: 'Vai atender conversas' })).toHaveAttribute('aria-checked', 'true')
    await user.click(within(dialog).getByLabelText('Recebe conversas da fila Cobrança'))
    await user.click(within(dialog).getByRole('button', { name: 'Aumentar: Conversas ao mesmo tempo em cada fila' }))
    await user.click(within(dialog).getByRole('button', { name: 'Enviar convite' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/team/invitations'))?.body).toEqual({
      email: 'agente.novo@k3g.com', role_key: 'tenant_agent', attends: true, queue_ids: ['q-fin'], queue_capacity: 2,
    }))
  })

  it('administrador e supervisor não atendem por padrão, e sem atender o convite não leva plano', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    const dialog = screen.getByRole('dialog', { name: 'Adicionar pessoa' })
    await user.type(within(dialog).getByLabelText('E-mail'), 'chefe@k3g.com')
    await user.click(within(dialog).getByRole('radio', { name: /Administrador/ }))
    expect(within(dialog).getByRole('switch', { name: 'Vai atender conversas' })).toHaveAttribute('aria-checked', 'false')
    expect(within(dialog).queryByLabelText('Recebe conversas da fila Cobrança')).toBeNull()
    await user.click(within(dialog).getByRole('button', { name: 'Enviar convite' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/team/invitations'))?.body).toEqual({ email: 'chefe@k3g.com', role_key: 'tenant_admin' }))
  })

  it('a ficha do convite diz o que a pessoa vai fazer ao aceitar', async () => {
    const user = userEvent.setup()
    serve(world({ invitations: [invitation('i7', 'plano@k3g.com', { attends: true, queue_ids: ['q-fin', 'q-sup'], queue_capacity: 2 })] }))
    page()
    await user.click(await rowOf('plano@k3g.com'))
    expect(within(sheet()).getByText('Ao aceitar, a pessoa já recebe conversas de Cobrança e Suporte N1, até 2 conversas ao mesmo tempo em cada fila.')).toBeInTheDocument()
  })

  it('sem os nomes das filas, a ficha do convite diz quantas filas o plano tem (não promete a fila padrão)', async () => {
    const user = userEvent.setup()
    serve(world({ permissions: ['membership.read', 'membership.manage'], invitations: [invitation('i9', 'semnomes@k3g.com', { attends: true, queue_ids: ['q-fin', 'q-sup'], queue_capacity: 2 })] }))
    page()
    await user.click(await rowOf('semnomes@k3g.com'))
    expect(within(sheet()).getByText('Ao aceitar, a pessoa já recebe conversas de 2 filas escolhidas no convite.')).toBeInTheDocument()
  })

  it('o convite aceita 0 = sem limite, e a ficha do convite diz isso', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ invitations: [invitation('i10', 'livre@k3g.com', { attends: true, queue_ids: ['q-fin'], queue_capacity: 0 })] }))
    page()
    await user.click(await rowOf('livre@k3g.com'))
    expect(within(sheet()).getByText('Ao aceitar, a pessoa já recebe conversas de Cobrança, sem limite de conversas ao mesmo tempo em cada fila.')).toBeInTheDocument()
    await user.click(within(sheet()).getByRole('button', { name: 'Fechar' }))

    await user.click(screen.getByRole('button', { name: /Adicionar pessoa/ }))
    const dialog = screen.getByRole('dialog', { name: 'Adicionar pessoa' })
    await user.type(within(dialog).getByLabelText('E-mail'), 'outro@k3g.com')
    await user.click(within(dialog).getByLabelText('Recebe conversas da fila Cobrança'))
    await user.click(within(dialog).getByRole('button', { name: 'Diminuir: Conversas ao mesmo tempo em cada fila' }))
    expect(within(dialog).getByRole('group', { name: 'Conversas ao mesmo tempo em cada fila' })).toHaveTextContent('sem limite')
    await user.click(within(dialog).getByRole('button', { name: 'Enviar convite' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/team/invitations'))?.body).toEqual({
      email: 'outro@k3g.com', role_key: 'tenant_agent', attends: true, queue_ids: ['q-fin'], queue_capacity: 0,
    }))
  })

  it('explica quando a conta de login não pôde ser preparada (502) e aponta o reenvio', async () => {
    const user = userEvent.setup()
    const w = world()
    serve(w)
    page()
    await user.click(await screen.findByRole('button', { name: /Adicionar pessoa/ }))
    await user.type(screen.getByLabelText('E-mail'), 'semlogin@k3g.com')
    w.failNext = { status: 502, data: 'invitation created but the sign-in account could not be prepared; use resend' }
    await user.click(screen.getByRole('button', { name: 'Enviar convite' }))
    expect(await screen.findByText(/a conta de login da pessoa não pôde ser preparada/)).toBeInTheDocument()
  })
})

describe('Pessoas: a ficha', () => {
  const save = async (user: ReturnType<typeof userEvent.setup>, s: HTMLElement) => {
    await user.click(within(s).getByRole('button', { name: 'Salvar alterações' }))
    return within(s).getByRole('alertdialog', { name: /^Aplicar as alterações em/ })
  }

  it('muda a função só depois de salvar e confirmar, e explica o conflito do último administrador', async () => {
    const user = userEvent.setup()
    const w = world()
    const calls = serve(w)
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    await user.click(within(s).getByRole('radio', { name: /Agente/ }))
    expect(within(s).getByText('1 alteração não salva')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    const confirm = await save(user, s)
    expect(within(confirm).getByText('Função: Administrador → Agente')).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    w.failNext = { status: 409 }
    await user.click(within(confirm).getByRole('button', { name: 'Aplicar alterações' }))
    expect(await within(s).findByText('A instância ficaria sem nenhum administrador ativo.')).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ role_key: 'tenant_agent' })
    expect(within(s).getByText('1 alteração não salva')).toBeInTheDocument() // nothing was saved: the change is still pending
  })

  it('liga o atendimento de quem não atende (POST /agents) e pausa quem atende (PATCH), só ao aplicar', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Dani Prado'))
    await user.click(within(sheet()).getByRole('button', { name: 'Passar a atender' }))
    expect(calls.some((c) => c.method === 'POST')).toBe(false)
    const first = await save(user, sheet())
    expect(within(first).getByText('Passa a receber conversas')).toBeInTheDocument()
    expect(within(first).getByText(/Sem fila escolhida: entra na fila padrão de transbordo/)).toBeInTheDocument()
    await user.click(within(first).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/agents'))?.body).toEqual({ membership_id: 'm-dani' }))
    expect(await within(sheet()).findByText('Alterações salvas.')).toBeInTheDocument()
    await user.click(within(sheet()).getByRole('button', { name: 'Fechar' }))

    await user.click(await rowOf('Ana Souza'))
    await user.click(within(sheet()).getByRole('switch', { name: 'Recebe conversas' }))
    const second = await save(user, sheet())
    expect(within(second).getByText('Fica em pausa: não recebe conversas novas (as filas ficam guardadas)')).toBeInTheDocument()
    await user.click(within(second).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/agents/p-ana'))?.body).toEqual({ status: 'disabled' }))
  })

  it('escolhe as filas por setor numa lista compacta e aplica tudo de uma vez: entra, disponibilidade e capacidade; depois sai', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    expect(within(s).getByRole('list', { name: 'Filas de Suporte' })).toBeInTheDocument()
    expect(within(s).getByRole('list', { name: 'Filas de Financeiro' })).toBeInTheDocument()
    await user.click(within(s).getByLabelText('Participa da fila Cobrança'))
    await user.click(within(s).getByRole('switch', { name: 'Disponível na fila Suporte N1' }))
    await user.click(within(s).getByRole('button', { name: 'Aumentar: Capacidade na fila Suporte N1' }))
    expect(calls.some((c) => c.method !== 'GET')).toBe(false)
    expect(within(s).getByText('2 alterações não salvas')).toBeInTheDocument()
    const confirm = await save(user, s)
    expect(within(confirm).getByText('Entra na fila Cobrança: disponível, até 1 conversa ao mesmo tempo')).toBeInTheDocument()
    expect(within(confirm).getByText('Fila Suporte N1: fica indisponível; até 2 → até 3 ao mesmo tempo')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/agents/p-ana/queues'))?.body).toEqual({ queue_id: 'q-fin', available: true, capacity: 1 }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/queues/qm-ana'))?.body).toEqual({ available: false, capacity: 3 }))
    expect(await within(s).findByText('Alterações salvas.')).toBeInTheDocument()

    await user.click(within(s).getByLabelText('Participa da fila Suporte N1'))
    await user.click(within(await save(user, s)).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/agents/p-ana/queues/qm-ana'))).toBe(true))
  })

  it('mexer na fila e pausar no mesmo salvamento aplica a fila antes da pausa', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    await user.click(within(s).getByRole('button', { name: 'Aumentar: Capacidade na fila Suporte N1' }))
    await user.click(within(s).getByRole('switch', { name: 'Recebe conversas' }))
    const confirm = await save(user, s)
    expect(within(confirm).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Fila Suporte N1: até 2 → até 3 ao mesmo tempo',
      'Fica em pausa: não recebe conversas novas (as filas ficam guardadas)',
    ])
    await user.click(within(confirm).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH').map((c) => c.url.replace(/^.*\/agents/, ''))).toEqual(['/p-ana/queues/qm-ana', '/p-ana']))
  })

  it('atendente novo: se a fila escolhida falhar, a nova tentativa entra nela e tira a fila padrão que o servidor pôs', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Dani Prado'))
    const s = sheet()
    await user.click(within(s).getByRole('button', { name: 'Passar a atender' }))
    await user.click(within(s).getByLabelText('Participa da fila Cobrança'))
    const realPost = vi.mocked(axios.post).getMockImplementation()!
    vi.mocked(axios.post).mockImplementation(async (url: string, body?: any) => {
      if (url.endsWith('/agents/p-dani/queues')) {
        vi.mocked(axios.post).mockImplementation(realPost)
        calls.push({ method: 'POST', url, body })
        return Promise.reject({ response: { status: 500, data: 'boom' } })
      }
      return realPost(url, body)
    })
    await user.click(within(await save(user, s)).getByRole('button', { name: 'Aplicar alterações' }))
    expect(await within(s).findByText('Não foi possível salvar. Tente de novo.')).toBeInTheDocument()
    const retry = await save(user, s)
    expect(within(retry).getAllByRole('listitem').map((li) => li.textContent)).toEqual([
      'Entra na fila Cobrança: disponível, até 1 conversa ao mesmo tempo',
      'Sai da fila Suporte N1',
    ])
    await user.click(within(retry).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/agents/p-dani/queues/qm-dani-def'))).toBe(true))
    expect(calls.filter((c) => c.method === 'POST' && c.url.endsWith('/agents'))).toHaveLength(1) // the attendant is created once
  })

  it('não deixa fechar a ficha no meio de um salvamento', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    await user.click(within(s).getByRole('radio', { name: /Agente/ }))
    let release: (v: unknown) => void = () => {}
    vi.mocked(axios.patch).mockImplementationOnce(() => new Promise((r) => { release = r }))
    await user.click(within(await save(user, s)).getByRole('button', { name: 'Aplicar alterações' }))
    await user.click(within(s).getByRole('button', { name: 'Fechar' }))
    expect(screen.getByRole('dialog', { name: /Ficha de/ })).toBeInTheDocument()
    expect(within(s).queryByRole('alertdialog', { name: 'Fechar sem salvar?' })).toBeNull()
    release({ data: {} })
    expect(await within(s).findByText('Alterações salvas.')).toBeInTheDocument()
  })

  it('aceita 0 = sem limite de conversas ao mesmo tempo', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    const less = within(s).getByRole('button', { name: 'Diminuir: Capacidade na fila Suporte N1' })
    await user.click(less)
    await user.click(less)
    expect(within(s).getByRole('group', { name: 'Capacidade na fila Suporte N1' })).toHaveTextContent('sem limite')
    expect(less).toBeDisabled()
    const confirm = await save(user, s)
    expect(within(confirm).getByText('Fila Suporte N1: até 2 → sem limite ao mesmo tempo')).toBeInTheDocument()
    await user.click(within(confirm).getByRole('button', { name: 'Aplicar alterações' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/queues/qm-ana'))?.body).toEqual({ available: true, capacity: 0 }))
  })

  it('descarta, volta da confirmação sem gravar e pergunta antes de fechar com alterações', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    await user.click(within(s).getByRole('radio', { name: /Agente/ }))
    await user.click(within(s).getByRole('button', { name: 'Descartar' }))
    expect(within(s).queryByText('1 alteração não salva')).toBeNull()
    expect(within(s).getByRole('radio', { name: /Administrador/ })).toBeChecked()

    await user.click(within(s).getByRole('radio', { name: /Agente/ }))
    await user.click(within(await save(user, s)).getByRole('button', { name: 'Voltar' }))
    expect(within(s).getByText('1 alteração não salva')).toBeInTheDocument()

    await user.click(within(s).getByRole('button', { name: 'Fechar' }))
    const leave = within(s).getByRole('alertdialog', { name: 'Fechar sem salvar?' })
    await user.click(within(leave).getByRole('button', { name: 'Continuar editando' }))
    expect(screen.getByRole('dialog', { name: /Ficha de/ })).toBeInTheDocument()
    await user.click(within(s).getByRole('button', { name: 'Fechar' }))
    await user.click(within(within(s).getByRole('alertdialog', { name: 'Fechar sem salvar?' })).getByRole('button', { name: 'Fechar sem salvar' }))
    expect(screen.queryByRole('dialog', { name: /Ficha de/ })).toBeNull()
    expect(calls.some((c) => c.method !== 'GET')).toBe(false)
  })

  it('se parte falhar, o que foi salvo sai da lista e o resto continua pendente', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Ana Souza'))
    const s = sheet()
    await user.click(within(s).getByRole('radio', { name: /Supervisor/ }))
    await user.click(within(s).getByLabelText('Participa da fila Cobrança'))
    vi.mocked(axios.post).mockImplementationOnce(async (url: string, body?: any) => {
      calls.push({ method: 'POST', url, body })
      return Promise.reject({ response: { status: 500, data: 'boom' } })
    })
    await user.click(within(await save(user, s)).getByRole('button', { name: 'Aplicar alterações' }))
    expect(await within(s).findByText('Não foi possível salvar. Tente de novo.')).toBeInTheDocument()
    expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ role_key: 'tenant_supervisor' })
    await waitFor(() => expect(within(s).getByText('1 alteração não salva')).toBeInTheDocument())
    const again = await save(user, s)
    expect(within(again).queryByText(/^Função:/)).toBeNull()
    expect(within(again).getByText(/^Entra na fila Cobrança/)).toBeInTheDocument()
  })

  it('mostra o que falta para receber conversas', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await user.click(await rowOf('Bia Lima'))
    const s = sheet()
    expect(within(s).getByText('Bia ainda não recebe conversas')).toBeInTheDocument()
    expect(within(s).getByText('Está em ao menos uma fila').closest('li')).toHaveTextContent('(falta)')
  })

  it('quem está em pausa mostra as filas guardadas e não deixa mexer nelas', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await user.click(await rowOf('Caio Reis'))
    const s = sheet()
    expect(within(s).getByText('Filas guardadas: Cobrança.')).toBeInTheDocument()
    expect(within(s).queryByLabelText('Participa da fila Cobrança')).toBeNull()
    expect(within(s).getByRole('button', { name: 'Voltar a atender' })).toBeInTheDocument()
  })

  it('suspende e remove só depois de confirmar', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('Bia Lima'))
    await user.click(within(sheet()).getByRole('button', { name: 'Suspender acesso' }))
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    const confirm = within(sheet()).getByRole('alertdialog', { name: 'Suspender o acesso de Bia Lima?' })
    await user.click(within(confirm).getByRole('button', { name: 'Suspender acesso' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/team/m-bia'))?.body).toEqual({ status: 'inactive' }))

    await user.click(within(sheet()).getByRole('button', { name: 'Remover da instância' }))
    const remove = within(sheet()).getByRole('alertdialog', { name: 'Remover Bia Lima desta instância?' })
    await user.click(within(remove).getByRole('button', { name: 'Remover da instância' }))
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH' && c.url.endsWith('/team/m-bia')).map((c) => c.body)).toContainEqual({ status: 'revoked' }))
  })

  it('o supervisor escolhe quem atende e as filas, mas não muda função nem acesso', async () => {
    const user = userEvent.setup()
    serve(world({ permissions: SUPERVISOR }))
    page()
    expect(await rowOf('Ana Souza')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: /Adicionar pessoa/ })).toBeNull()
    await user.click(await rowOf('Bia Lima'))
    const s = sheet()
    expect(within(s).queryByRole('radio')).toBeNull()
    expect(within(s).queryByRole('button', { name: 'Suspender acesso' })).toBeNull()
    expect(within(s).getByRole('switch', { name: 'Recebe conversas' })).toBeEnabled()
    expect(within(s).getByLabelText('Participa da fila Cobrança')).toBeEnabled()
  })

  it('mostra a escala da pessoa ou o padrão de 24 h', async () => {
    const user = userEvent.setup()
    serve(world({ shifts: [{ user_id: 'u-ana', calendar_id: 'c1', sector_id: 's-sup' }] }))
    page()
    await user.click(await rowOf('Ana Souza'))
    expect(await within(sheet()).findByText('Comercial no setor Suporte')).toBeInTheDocument()
  })

  it('abre direto a ficha pedida no endereço', async () => {
    serve(world())
    page('/settings/people?pessoa=m-bia')
    expect(await screen.findByRole('dialog', { name: 'Ficha de Bia Lima' })).toBeInTheDocument()
  })
})

describe('Pessoas: a ficha de um convite', () => {
  it('reenvia e cancela (com confirmação) um convite pendente', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await rowOf('nova@k3g.com'))
    const s = sheet()
    await user.click(within(s).getByRole('button', { name: 'Reenviar convite' }))
    await waitFor(() => expect(calls.some((c) => c.method === 'POST' && c.url.endsWith('/team/invitations/i1/resend'))).toBe(true))
    expect(await within(s).findByText('Convite reenviado.')).toBeInTheDocument()
    await user.click(within(s).getByRole('button', { name: 'Cancelar convite' }))
    const confirm = within(s).getByRole('alertdialog', { name: 'Cancelar o convite de nova@k3g.com?' })
    await user.click(within(confirm).getByRole('button', { name: 'Cancelar convite' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/team/invitations/i1'))?.body).toEqual({ status: 'revoked' }))
  })

  it('um convite vencido também pode ser cancelado (desliga a conta de login que ninguém usou)', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ invitations: [invitation('i8', 'vencido@k3g.com', { status: 'expired', expires_at: '2020-01-01T00:00:00Z' })] }))
    page()
    await user.click(await rowOf('vencido@k3g.com'))
    const s = sheet()
    await user.click(within(s).getByRole('button', { name: 'Cancelar convite' }))
    await user.click(within(within(s).getByRole('alertdialog')).getByRole('button', { name: 'Cancelar convite' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/team/invitations/i8'))?.body).toEqual({ status: 'revoked' }))
  })

  it('marca o convite que não chegou e oferece o link quando o servidor dá um', async () => {
    const user = userEvent.setup()
    serve(world({ invitations: [invitation('i3', 'sem-email@k3g.com', { sent_at: undefined, invite_url: '/invite/abc' })] }))
    page()
    const row = await rowOf('sem-email@k3g.com')
    expect(within(row).getByText('Convite não entregue')).toBeInTheDocument()
    await user.click(row)
    expect(within(sheet()).getByRole('button', { name: 'Copiar link (dev)' })).toBeInTheDocument()
  })

  it('não oferece reenvio sem envio configurado', async () => {
    const user = userEvent.setup()
    serve(world({ delivery: false }))
    page()
    await user.click(await rowOf('nova@k3g.com'))
    expect(within(sheet()).queryByRole('button', { name: 'Reenviar convite' })).toBeNull()
    expect(within(sheet()).getByText('O envio de convites não está configurado neste ambiente.')).toBeInTheDocument()
  })
})
