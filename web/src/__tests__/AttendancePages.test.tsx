import { beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import AttendanceSectorsPage from '../pages/AttendanceSectorsPage'
import AttendanceCalendarsPage from '../pages/AttendanceCalendarsPage'
import AttendanceShiftsPage from '../pages/AttendanceShiftsPage'
import AttendanceOverflowPage from '../pages/AttendanceOverflowPage'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const ADMIN = ['agent.read', 'agent.manage', 'tenant.manage', 'routing.override']
const READ_ONLY = ['agent.read']

interface World {
  permissions: string[]
  sectors: any[]
  queues: any[]
  calendars: any[]
  agents: any[]
  shifts: any[]
  rosters: any[]
  policies: Record<string, any>
  sla: any[]
  contracts: any[]
  notices: Record<string, string>
  openNotices: Record<string, string>
  failNext?: { status: number; data: string } | null
}
type Call = { method: string; url: string; body?: any; params?: any }

const queue = (id: string, name: string) => ({ id, name, mode: 'manual', is_default: false, member_count: 0, available_count: 0, open_conversation_count: 0, created_at: '', updated_at: '' })
const agent = (id: string, name: string) => ({ id: 'p-' + id, membership_id: 'm-' + id, user_id: id, name, email: `${id}@k3g.com`, role: 'Agente', status: 'active', queues: [] })

function world(over: Partial<World> = {}): World {
  return {
    permissions: ADMIN,
    sectors: [
      { id: 's-fin', name: 'Financeiro', active: true, calendar_id: 'c-bus', queues: [{ id: 'q-fin', name: 'Cobrança' }] },
      { id: 's-sup', name: 'Suporte', active: true, queues: [] },
    ],
    queues: [queue('q-fin', 'Cobrança'), queue('q-sup', 'Suporte N1'), queue('q-loose', 'Avulso')],
    calendars: [{ id: 'c-bus', name: 'Comercial', kind: 'business', timezone: 'America/Manaus', revision: 3, in_use: 1, updated_at: '2026-10-01T00:00:00Z' }, { id: 'c-24', name: '24 horas', kind: '24x7', timezone: 'America/Manaus', revision: 1, in_use: 0, updated_at: '2026-10-01T00:00:00Z' }],
    agents: [agent('u-ana', 'Ana Souza'), agent('u-bia', 'Bia Lima')],
    shifts: [],
    rosters: [{ id: 'r1', name: 'Plantão noite', kind: 'on_call', active: true, on_now: 0 }],
    policies: {},
    sla: [{ scope_kind: 'tenant', risk_percent: 80, counting: 'wall', active: true, first_response_seconds: 300, revision: 1 }],
    contracts: [{ contract_id: 'ct1', hub_name: 'Hub K3G', status: 'active', valid_from: '2026-01-01T00:00:00Z' }],
    notices: {},
    openNotices: {},
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
    return Promise.reject({ response: { status: f.status, data: f.data } })
  }
  const open = { open: true, at: '2026-10-13T12:00:00Z', calendar_source: 'sector', timezone: 'America/Manaus', next_close: '2026-10-13T22:00:00Z' }
  vi.mocked(axios.get).mockImplementation(async (url: string, cfg?: any) => {
    if (url.endsWith('/me/access')) return ok({ role_key: 'tenant_admin', permissions: w.permissions, invitation_delivery_available: true })
    if (url.endsWith('/sectors')) return ok({ items: w.sectors })
    if (url.endsWith('/queues')) return ok({ items: w.queues, count: w.queues.length })
    if (url.endsWith('/calendars')) return ok({ items: w.calendars, default_timezone: 'America/Manaus', default_calendar_id: null })
    if (/\/calendars\/[^/]+$/.test(url)) return ok({ id: 'c-bus', name: 'Comercial', revision: 3, updated_at: '', suspensions: [], definition: { kind: 'business', timezone: 'America/Manaus', weekly: [{ day: 'mon', intervals: [{ start: '08:00', end: '18:00' }] }] } })
    if (url.endsWith('/availability')) return ok(url.includes('q-fin') ? open : { ...open, open: false, next_open: '2026-10-14T12:00:00Z', next_close: undefined })
    if (url.endsWith('/open-notice')) return ok({ queue_id: 'x', text: w.openNotices[url.match(/queues\/([^/]+)\//)![1]] ?? '' })
    if (url.endsWith('/closed-notice')) return ok({ queue_id: 'x', text: w.notices[url.match(/queues\/([^/]+)\//)![1]] ?? '' })
    if (url.endsWith('/agents')) return ok({ items: w.agents })
    if (url.endsWith('/shifts')) return ok({ items: w.shifts })
    if (url.endsWith('/on-call/rosters')) return ok({ items: w.rosters })
    if (url.endsWith('/periods')) return ok({ items: [] })
    if (url.endsWith('/overflow-policy')) return ok({ policy: w.policies[url.match(/queues\/([^/]+)\//)![1]] ?? null })
    if (url.endsWith('/eligibility')) return ok({ queue_id: 'q', at: '', open: true, presence_required: true, eligible_count: 1, calendar_only: false, agents: [
      { user_id: 'u-ana', name: 'Ana Souza', eligible: true, reasons: [], on_call: false, workload: 1, capacity: 5, shift_source: 'none' },
      { user_id: 'u-bia', name: 'Bia Lima', eligible: false, reasons: ['offline', 'no_capacity'], on_call: false, workload: 5, capacity: 5, shift_source: 'all' }] })
    if (url.endsWith('/hub-contracts')) return ok({ items: w.contracts })
    if (url.endsWith('/flows')) return ok({ items: [{ id: 'f1', name: 'Atendimento fora do horário', type: 'AFTER_HOURS', status: 'published', slug: 'ah' }, { id: 'f2', name: 'Boas-vindas', type: 'INBOUND', status: 'published', slug: 'bv' }, { id: 'f3', name: 'Sub', type: 'SUBFLOW', status: 'published', slug: 's' }] })
    if (url.endsWith('/sla-policies')) return ok({ items: w.sla })
    if (url.endsWith('/sla')) return ok({ queue_id: 'q', counts: { at_risk: 2, running: 3 }, items: [] })
    void cfg
    return Promise.reject({ response: { status: 404, data: 'not found' } })
  })
  vi.mocked(axios.post).mockImplementation(async (url: string, body: any) => {
    calls.push({ method: 'POST', url, body })
    const f = fail(); if (f) return f
    if (url.endsWith('/sectors')) { const s = { id: 's-new', name: body.name, active: true, queues: [] }; w.sectors.push(s); return ok(s) }
    if (url.endsWith('/queues')) { const q = { ...queue('q-new', body.name), mode: body.mode, is_default: body.is_default }; w.queues.push(q); return ok(q) }
    if (url.endsWith('/calendars')) return ok({ id: 'c-new', name: body.name, revision: 1, definition: body.definition, suspensions: [], updated_at: '' })
    if (url.endsWith('/suspensions')) return ok({ id: 'sus1', from: body.from, to: body.to, reason: body.reason })
    if (url.endsWith('/on-call/rosters')) { const r = { id: 'r-new', ...body, active: true, on_now: 0 }; w.rosters.push(r); return ok(r) }
    return ok({})
  })
  vi.mocked(axios.put).mockImplementation(async (url: string, body: any) => {
    calls.push({ method: 'PUT', url, body })
    const f = fail(); if (f) return f
    if (url.endsWith('/overflow-policy')) { const id = url.match(/queues\/([^/]+)\//)![1]; w.policies[id] = { ...body, revision: (w.policies[id]?.revision ?? 0) + 1 }; return ok({ policy: w.policies[id] }) }
    if (url.endsWith('/open-notice')) { w.openNotices[url.match(/queues\/([^/]+)\//)![1]] = body.text; return ok(body) }
    if (url.endsWith('/closed-notice')) { w.notices[url.match(/queues\/([^/]+)\//)![1]] = body.text; return ok(body) }
    return ok(body)
  })
  vi.mocked(axios.patch).mockImplementation(async (url: string, body: any) => {
    calls.push({ method: 'PATCH', url, body })
    const f = fail(); if (f) return f
    return ok({})
  })
  vi.mocked(axios.delete).mockImplementation(async (url: string) => {
    calls.push({ method: 'DELETE', url })
    const f = fail(); if (f) return f
    return ok(undefined)
  })
  return calls
}

beforeEach(() => {
  vi.clearAllMocks()
  localStorage.clear()
  setSession()
})

describe('Atendimento › Setores', () => {
  const page = () => renderAt(<AttendanceSectorsPage />, '/settings/attendance/sectors', '/settings/attendance/sectors')

  it('mostra cada setor com suas filas, se estão abertas agora e o que ainda não tem setor', async () => {
    serve(world())
    page()
    expect(await screen.findByRole('heading', { name: /Financeiro/ })).toBeInTheDocument()
    const list = await screen.findByRole('list', { name: 'Filas de Financeiro' })
    expect(within(list).getByText('Cobrança')).toBeInTheDocument()
    await waitFor(() => expect(within(list).getByText(/Aberto agora/)).toBeInTheDocument())
    expect(screen.getByText(/Nenhuma fila neste setor/)).toBeInTheDocument()
    const loose = await screen.findByText('Filas sem setor')
    expect(loose).toBeInTheDocument()
    expect(screen.getAllByText('Avulso').length).toBeGreaterThan(0)
    await waitFor(() => expect(screen.getAllByText(/Fechado · abre/).length).toBeGreaterThan(0))
  })

  it('cria setor, coloca uma fila avulsa em um setor e desativa com confirmação', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: '+ Novo setor' }))
    await user.type(screen.getByLabelText('Nome do setor'), 'Jurídico')
    await user.click(screen.getByRole('button', { name: 'Criar setor' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/sectors'))?.body).toEqual({ name: 'Jurídico' }))

    await user.selectOptions(await screen.findByLabelText('Colocar Avulso em um setor'), 's-fin')
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/queues/q-loose/sector'))?.body).toEqual({ sector_id: 's-fin' }))

    await user.click((await screen.findAllByRole('button', { name: 'Desativar' }))[0])
    expect(screen.getByText(/seguem a saída “indisponível”/)).toBeInTheDocument()
    expect(calls.some((c) => c.method === 'PATCH')).toBe(false)
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Desativar' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH')?.body).toEqual({ active: false }))
  })

  it('salva os avisos de horário por fila (fechada e ao abrir), só o que mudou, e explica o placeholder', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ notices: { 'q-fin': 'Voltamos já.' }, openNotices: { 'q-fin': 'Abrimos.' } }))
    page()
    const list = await screen.findByRole('list', { name: 'Filas de Financeiro' })
    await user.click(within(list).getByRole('button', { name: 'Avisos de horário' }))
    const closed = await screen.findByLabelText('Quando a fila está fechada')
    const opened = await screen.findByLabelText('Quando a fila abrir')
    await waitFor(() => { expect(closed).toHaveValue('Voltamos já.'); expect(opened).toHaveValue('Abrimos.') })
    expect(screen.getByText(/\{\{proxima_abertura\}\}/)).toBeInTheDocument()
    fireEvent.change(opened, { target: { value: 'Já estamos atendendo. Um atendente responderá em instantes.' } })
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/queues/q-fin/open-notice'))?.body).toEqual({ text: 'Já estamos atendendo. Um atendente responderá em instantes.' }))
    // the closed text did not change: it is not sent again
    expect(calls.some((c) => c.method === 'PUT' && c.url.endsWith('/queues/q-fin/closed-notice'))).toBe(false)
  })

  it('o aviso de fechado continua salvando pelo mesmo modal', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ notices: { 'q-fin': 'Voltamos já.' } }))
    page()
    const list = await screen.findByRole('list', { name: 'Filas de Financeiro' })
    await user.click(within(list).getByRole('button', { name: 'Avisos de horário' }))
    const box = await screen.findByLabelText('Quando a fila está fechada')
    await waitFor(() => expect(box).toHaveValue('Voltamos já.'))
    fireEvent.change(box, { target: { value: 'Fechado. Voltamos {{proxima_abertura}}.' } })
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/queues/q-fin/closed-notice'))?.body).toEqual({ text: 'Fechado. Voltamos {{proxima_abertura}}.' }))
    expect(calls.some((c) => c.method === 'PUT' && c.url.endsWith('/queues/q-fin/open-notice'))).toBe(false)
  })

  it('quem só lê não vê nenhuma ação, e um erro do servidor vira frase em português', async () => {
    serve(world({ permissions: READ_ONLY }))
    const view = page()
    await screen.findByRole('heading', { name: /Financeiro/ })
    expect(screen.queryByRole('button', { name: '+ Novo setor' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Renomear' })).toBeNull()
    view.unmount()

    const user = userEvent.setup()
    const w = world()
    serve(w)
    page()
    await user.click(await screen.findByRole('button', { name: '+ Novo setor' }))
    await user.type(screen.getByLabelText('Nome do setor'), 'Financeiro')
    w.failNext = { status: 409, data: 'a sector with this name already exists' }
    await user.click(screen.getByRole('button', { name: 'Criar setor' }))
    expect(await screen.findByText(/Já existe um item com esse nome/)).toBeInTheDocument()
  })

  it('sem permissão de leitura mostra o estado de acesso restrito', async () => {
    serve(world({ permissions: [] }))
    page()
    expect(await screen.findByText(/Você não tem permissão para ver a configuração de atendimento/)).toBeInTheDocument()
    // no read is even attempted for the configuration (the only request is the permission list itself)
    const urls = vi.mocked(axios.get).mock.calls.map((c) => String(c[0]))
    expect(urls.filter((u) => !u.endsWith('/me/access'))).toEqual([])
  })
})

describe('Atendimento › Filas (dentro de Filas e setores)', () => {
  const page = () => renderAt(<AttendanceSectorsPage />, '/settings/attendance/sectors', '/settings/attendance/sectors')
  const withCounts = (q: any, over: any) => ({ ...q, ...over })
  const menu = async (user: ReturnType<typeof userEvent.setup>, queueName: string, item: string) => {
    await user.click(await screen.findByRole('button', { name: `Mais ações da fila ${queueName}` }))
    await user.click(screen.getByRole('menuitem', { name: item }))
  }

  it('mostra a estratégia, a fila padrão e as contagens reais, e avisa quando ninguém está na fila', async () => {
    serve(world({ queues: [withCounts(queue('q-fin', 'Cobrança'), { mode: 'round_robin', is_default: true, member_count: 3, available_count: 2, open_conversation_count: 4 }), queue('q-sup', 'Suporte N1'), queue('q-loose', 'Avulso')] }))
    page()
    const list = await screen.findByRole('list', { name: 'Filas de Financeiro' })
    await waitFor(() => expect(within(list).getByText('3 pessoas · 2 disponíveis · 4 conversas abertas')).toBeInTheDocument())
    expect(within(list).getByText('Padrão')).toBeInTheDocument()
    expect(within(list).getByText('Rodízio')).toBeInTheDocument()
    expect(screen.getAllByText('Ninguém nesta fila: as conversas dela vão esperar.').length).toBeGreaterThan(0)
  })

  it('cria uma fila já como padrão e dentro de um setor', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(await screen.findByRole('button', { name: '+ Nova fila' }))
    const dialog = screen.getByRole('dialog')
    await user.type(within(dialog).getByLabelText('Nome da fila'), 'Retenção')
    await user.click(within(dialog).getByLabelText('Rodízio'))
    await user.selectOptions(within(dialog).getByLabelText('Setor'), 's-sup')
    await user.click(within(dialog).getByLabelText(/Usar como fila padrão/))
    await user.click(within(dialog).getByRole('button', { name: 'Criar fila' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/queues'))?.body).toEqual({ name: 'Retenção', mode: 'round_robin', is_default: true }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/queues/q-new/sector'))?.body).toEqual({ sector_id: 's-sup' }))
  })

  it('renomeia, troca a estratégia e define a fila padrão pelo menu da fila', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await menu(user, 'Cobrança', 'Renomear')
    const name = screen.getByLabelText('Nome da fila')
    await user.clear(name)
    await user.type(name, 'Cobrança PJ')
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/queues/q-fin'))?.body).toEqual({ name: 'Cobrança PJ' }))

    await menu(user, 'Cobrança', 'Estratégia: Rodízio')
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH' && c.url.endsWith('/queues/q-fin')).map((c) => c.body)).toContainEqual({ mode: 'round_robin' }))

    await menu(user, 'Cobrança', 'Definir como fila padrão')
    await waitFor(() => expect(calls.filter((c) => c.method === 'PATCH' && c.url.endsWith('/queues/q-fin')).map((c) => c.body)).toContainEqual({ is_default: true }))
  })

  it('pergunta antes de excluir e não oferece excluir a fila padrão', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ queues: [withCounts(queue('q-fin', 'Cobrança'), { is_default: true }), queue('q-sup', 'Suporte N1'), queue('q-loose', 'Avulso')] }))
    page()
    await user.click(await screen.findByRole('button', { name: 'Mais ações da fila Cobrança' }))
    expect(screen.getByRole('menuitem', { name: 'Excluir (troque a padrão antes)' })).toBeDisabled()
    await user.keyboard('{Escape}')

    await menu(user, 'Avulso', 'Excluir')
    expect(calls.some((c) => c.method === 'DELETE')).toBe(false)
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Excluir' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'DELETE')?.url).toMatch(/\/queues\/q-loose$/))
  })

  it('avisa quando nenhuma fila é a padrão', async () => {
    serve(world())
    page()
    expect(await screen.findByText(/Nenhuma fila é a padrão/)).toBeInTheDocument()
  })

  it('transforma a recusa do servidor numa frase e explica a estratégia Menor carga sem o motor', async () => {
    const user = userEvent.setup()
    const w = world()
    serve(w)
    page()
    w.failNext = { status: 409, data: 'least_loaded requires the routing engine' }
    await menu(user, 'Cobrança', 'Estratégia: Menor carga')
    expect(await screen.findByText(/Menor carga exige o motor de roteamento/)).toBeInTheDocument()
  })

  it('pessoas da fila: entra, sai e muda a disponibilidade de quem já atende', async () => {
    const user = userEvent.setup()
    const w = world({ agents: [
      { ...agent('u-ana', 'Ana Souza'), queues: [{ id: 'qm-ana', queue_id: 'q-fin', queue_name: 'Cobrança', available: true, capacity: 2 }] },
      agent('u-bia', 'Bia Lima'),
      { ...agent('u-caio', 'Caio Reis'), status: 'disabled' },
    ] })
    const calls = serve(w)
    page()
    await user.click(await screen.findByRole('button', { name: 'Pessoas da fila Cobrança' }))
    const dialog = await screen.findByRole('dialog', { name: /Pessoas da fila Cobrança/ })
    await user.click(await within(dialog).findByLabelText('Bia Lima participa da fila Cobrança'))
    await waitFor(() => expect(calls.find((c) => c.method === 'POST' && c.url.endsWith('/agents/p-u-bia/queues'))?.body).toEqual({ queue_id: 'q-fin', available: true, capacity: 1 }))
    await user.click(within(dialog).getByRole('switch', { name: 'Ana Souza disponível na fila Cobrança' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PATCH' && c.url.endsWith('/agents/p-u-ana/queues/qm-ana'))?.body).toEqual({ available: false, capacity: 2 }))
    await user.click(within(dialog).getByLabelText('Ana Souza participa da fila Cobrança'))
    await waitFor(() => expect(calls.some((c) => c.method === 'DELETE' && c.url.endsWith('/agents/p-u-ana/queues/qm-ana'))).toBe(true))
    expect(within(dialog).getByLabelText('Caio Reis participa da fila Cobrança')).toBeDisabled()
    expect(within(dialog).getByText(/Em pausa: reative em Pessoas/)).toBeInTheDocument()
  })

  it('quem só lê vê as filas e as pessoas, mas nenhuma ação', async () => {
    const user = userEvent.setup()
    serve(world({ permissions: READ_ONLY, agents: [{ ...agent('u-ana', 'Ana Souza'), queues: [{ id: 'qm-ana', queue_id: 'q-fin', queue_name: 'Cobrança', available: true, capacity: 2 }] }] }))
    page()
    await screen.findByRole('list', { name: 'Filas de Financeiro' })
    expect(screen.queryByRole('button', { name: '+ Nova fila' })).toBeNull()
    expect(screen.queryByRole('button', { name: /Mais ações da fila/ })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Pessoas da fila Cobrança' }))
    expect(await screen.findByLabelText('Ana Souza participa da fila Cobrança')).toBeDisabled()
  })
})

describe('Atendimento › Horários', () => {
  const page = () => renderAt(<AttendanceCalendarsPage />, '/settings/attendance/calendars', '/settings/attendance/calendars')

  it('lista os horários, bloqueia remover o que está em uso e cria um comercial', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    const bus = await screen.findByLabelText('Horário Comercial')
    expect(within(bus).getByText(/usado por 1 item/)).toBeInTheDocument()
    expect(within(bus).getByRole('button', { name: 'Remover' })).toBeDisabled()
    expect(within(screen.getByLabelText('Horário 24 horas')).getByRole('button', { name: 'Remover' })).toBeEnabled()

    await user.click(screen.getByRole('button', { name: '+ Novo horário' }))
    const create = screen.getByRole('button', { name: 'Criar horário' })
    expect(create).toBeDisabled() // sem nome
    expect(screen.getByText(/O nome precisa ter de 1 a 60 caracteres/)).toBeInTheDocument()
    await user.type(screen.getByLabelText('Nome do horário'), 'Plantão fim de semana')
    await user.selectOptions(screen.getByLabelText('Tipo'), 'custom')
    expect(screen.getByRole('button', { name: 'Criar horário' })).toBeEnabled()
    await user.click(screen.getByRole('button', { name: 'Criar horário' }))
    await waitFor(() => {
      const c = calls.find((x) => x.method === 'POST' && x.url.endsWith('/calendars'))
      expect(c?.body.name).toBe('Plantão fim de semana')
      expect(c?.body.definition.kind).toBe('custom')
      expect(c?.body.definition.timezone).toBe('America/Manaus')
    })
  })

  it('a suspensão de emergência é enviada como instante no fuso do horário, não no do navegador', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await user.click(within(await screen.findByLabelText('Horário Comercial')).getByRole('button', { name: 'Editar' }))
    fireEvent.change(await screen.findByLabelText('Suspender de'), { target: { value: '2026-10-14T09:00' } })
    fireEvent.change(screen.getByLabelText('Suspender até'), { target: { value: '2026-10-14T12:00' } })
    await user.type(screen.getByLabelText('Motivo'), 'Queda de energia')
    await user.click(screen.getByRole('button', { name: 'Suspender' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/calendars/c-bus/suspensions'))?.body).toEqual({ from: '2026-10-14T13:00:00.000Z', to: '2026-10-14T16:00:00.000Z', reason: 'Queda de energia' }))
  })

  it('salvar um horário envia a revisão lida (outra pessoa mexeu = conflito explicado)', async () => {
    const user = userEvent.setup()
    const w = world()
    const calls = serve(w)
    page()
    await user.click(within(await screen.findByLabelText('Horário Comercial')).getByRole('button', { name: 'Editar' }))
    w.failNext = { status: 409, data: 'calendar changed since it was read' }
    await user.click(await screen.findByRole('button', { name: 'Salvar alterações' }))
    expect(calls.find((c) => c.method === 'PUT')?.body.expected_revision).toBe(3)
    expect(await screen.findByText(/Outra pessoa alterou isto enquanto você editava/)).toBeInTheDocument()
  })

  it('quem só lê vê o horário mas não pode alterá-lo', async () => {
    serve(world({ permissions: READ_ONLY }))
    page()
    const bus = await screen.findByLabelText('Horário Comercial')
    expect(within(bus).getByRole('button', { name: 'Ver' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: '+ Novo horário' })).toBeNull()
    expect(within(bus).queryByRole('button', { name: 'Remover' })).toBeNull()
  })
})

describe('Atendimento › Escalas e plantões', () => {
  const page = () => renderAt(<AttendanceShiftsPage />, '/settings/attendance/shifts', '/settings/attendance/shifts')

  it('quem não tem escala aparece como 24 h (padrão); define e remove escala por setor', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ shifts: [{ user_id: 'u-bia', calendar_id: 'c-bus', sector_id: 's-fin' }] }))
    page()
    const ana = await screen.findByLabelText('Escala de Ana Souza')
    expect(within(ana).getByText('24 h (padrão)')).toBeInTheDocument()
    const bia = screen.getByLabelText('Escala de Bia Lima')
    expect(within(bia).getByText(/Comercial — setor Financeiro/)).toBeInTheDocument()

    await user.selectOptions(within(ana).getByLabelText('Escala para Ana Souza'), 'c-bus')
    await user.selectOptions(within(ana).getByLabelText('Escopo da escala de Ana Souza'), 's:s-fin')
    await user.click(within(ana).getByRole('button', { name: 'Definir escala' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/shifts') && c.body.user_id === 'u-ana')?.body).toEqual({ user_id: 'u-ana', calendar_id: 'c-bus', sector_id: 's-fin' }))

    await user.click(within(bia).getByRole('button', { name: 'Remover' }))
    await waitFor(() => expect(calls.find((c) => c.body?.user_id === 'u-bia')?.body).toEqual({ user_id: 'u-bia', calendar_id: null, sector_id: 's-fin' }))
  })

  it('lista plantões, mostra quem está de plantão agora e cria uma lista nova', async () => {
    const user = userEvent.setup()
    const calls = serve(world({ rosters: [{ id: 'r1', name: 'Plantão noite', kind: 'on_call', active: true, on_now: 0 }, { id: 'r2', name: 'Contingência TI', kind: 'contingency', active: true, on_now: 2 }] }))
    page()
    const noite = await screen.findByLabelText('Plantão Plantão noite')
    expect(within(noite).getByText('Ninguém de plantão agora')).toBeInTheDocument()
    expect(within(screen.getByLabelText('Plantão Contingência TI')).getByText('2 de plantão agora')).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '+ Nova lista de plantão' }))
    await user.type(screen.getByLabelText('Nome'), 'Sobreaviso')
    await user.click(screen.getByRole('button', { name: 'Criar' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/on-call/rosters'))?.body).toEqual({ name: 'Sobreaviso', kind: 'on_call', sector_id: null }))
  })
})

describe('Atendimento › Transbordo e SLA', () => {
  const page = () => renderAt(<AttendanceOverflowPage />, '/settings/attendance/overflow', '/settings/attendance/overflow')

  it('mostra a situação agora: quem pode atender e por que os outros não', async () => {
    serve(world())
    page()
    expect(await screen.findByText('1 atendente pode receber agora')).toBeInTheDocument()
    expect(screen.getByText(/offline, capacidade esgotada · 5\/5/)).toBeInTheDocument()
    expect(screen.getByText(/elegível · 1\/5/)).toBeInTheDocument()
  })

  it('monta uma cadeia, bloqueia ciclo, salva com a revisão e mantém o histórico explicado', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await screen.findByText(/Este grupo não tem transbordo/)
    await user.click(screen.getByRole('button', { name: '+ Etapa' }))
    expect(screen.getByLabelText('Etapa 1')).toBeInTheDocument()
    // o primeiro destino sugerido nunca é o próprio grupo de origem (Cobrança)
    await user.selectOptions(screen.getByLabelText('Escolha o destino da etapa 1'), 'q-sup')
    await user.click(screen.getByRole('button', { name: 'Salvar política' }))
    await waitFor(() => {
      const put = calls.find((c) => c.method === 'PUT' && c.url.endsWith('/queues/q-fin/overflow-policy'))
      expect(put?.body.steps).toHaveLength(1)
      expect(put?.body.steps[0]).toMatchObject({ position: 1, destination_kind: 'queue', destination_id: 'q-sup', triggers: ['after_hours', 'no_eligible_agent'] })
    })
    expect(await screen.findByText(/reavaliadas com a nova política/)).toBeInTheDocument()
    // o grupo de origem não é oferecido como destino
    expect(within(screen.getByLabelText('Escolha o destino da etapa 1')).queryByRole('option', { name: 'Cobrança' })).toBeNull()
  })

  it('contingência (entregar com destino fechado / fora da escala) só aparece para quem tem a permissão', async () => {
    const user = userEvent.setup()
    serve(world({ permissions: ['agent.read', 'agent.manage'], policies: { 'q-fin': { id: 'p', active: true, max_steps: 4, on_exhausted: 'manual', revision: 1, steps: [{ position: 1, triggers: ['after_hours'], wait_seconds: 0, wait_business: false, destination_kind: 'queue', destination_id: 'q-sup', allow_closed: false, allow_off_shift: false }] } } }))
    const view = page()
    await screen.findByLabelText('Etapa 1')
    expect(screen.queryByLabelText(/Entregar mesmo com o destino fechado/)).toBeNull()
    view.unmount()
    serve(world({ policies: { 'q-fin': { id: 'p', active: true, max_steps: 4, on_exhausted: 'manual', revision: 1, steps: [{ position: 1, triggers: ['after_hours'], wait_seconds: 0, wait_business: false, destination_kind: 'queue', destination_id: 'q-sup', allow_closed: false, allow_off_shift: false }] } } }))
    page()
    await screen.findByLabelText('Etapa 1')
    expect(await screen.findByLabelText(/Entregar mesmo com o destino fechado/)).toBeInTheDocument()
    void user
  })

  it('destino Hub lista só contratos (nunca operadores) e exige o grupo que o Hub atende', async () => {
    const user = userEvent.setup()
    serve(world())
    page()
    await screen.findByText(/Este grupo não tem transbordo/)
    await user.click(screen.getByRole('button', { name: '+ Etapa' }))
    await user.selectOptions(screen.getByLabelText('Destino da etapa 1'), 'hub')
    const dest = screen.getByLabelText('Escolha o destino da etapa 1')
    expect(within(dest).getByRole('option', { name: 'Hub K3G' })).toBeInTheDocument()
    await user.selectOptions(dest, 'ct1')
    expect(screen.getByText(/A conversa continua dentro da sua empresa/)).toBeInTheDocument()
  })

  it('destino Fluxo oferece só fluxos que uma etapa pode iniciar e explica quando é pulado', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    await screen.findByText(/Este grupo não tem transbordo/)
    await user.click(screen.getByRole('button', { name: '+ Etapa' }))
    await user.selectOptions(screen.getByLabelText('Destino da etapa 1'), 'flow')
    const dest = screen.getByLabelText('Escolha o destino da etapa 1')
    expect(await within(dest).findByRole('option', { name: 'Atendimento fora do horário (fora do horário)' })).toBeInTheDocument()
    expect(within(dest).getByRole('option', { name: 'Boas-vindas' })).toBeInTheDocument()
    expect(within(dest).queryByRole('option', { name: 'Sub' })).toBeNull()
    expect(screen.getByText(/Só inicia se nenhum fluxo estiver em execução nela/)).toBeInTheDocument()
    await user.selectOptions(dest, 'f1')
    await user.click(screen.getByRole('button', { name: 'Salvar política' }))
    await waitFor(() => expect(calls.find((c) => c.method === 'PUT' && c.url.endsWith('/overflow-policy'))?.body.steps[0]).toMatchObject({ destination_kind: 'flow', destination_id: 'f1' }))
  })

  it('mostra as metas de SLA e salva uma nova em segundos', async () => {
    const user = userEvent.setup()
    const calls = serve(world())
    page()
    expect(await screen.findByLabelText('Meta Toda a empresa')).toBeInTheDocument()
    expect(screen.getByText(/1ª resposta 5 min/)).toBeInTheDocument()
    expect(await screen.findByText(/em risco: 2/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: '+ Nova meta' }))
    fireEvent.change(screen.getByLabelText('Primeira resposta (minutos)'), { target: { value: '10' } })
    fireEvent.change(screen.getByLabelText('Resolução (minutos)'), { target: { value: '240' } })
    await user.click(screen.getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(calls.find((c) => c.url.endsWith('/sla-policies'))?.body).toMatchObject({ scope_kind: 'tenant', first_response_seconds: 600, resolution_seconds: 14400, risk_percent: 80, counting: 'wall', active: true }))
  })
})
