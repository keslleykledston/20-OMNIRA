import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import FlowsPage from '../pages/FlowsPage'
import type { Flow, PackInfo, RunDetail, RunSummary, TemplateInfo } from '../lib/flows'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const flow = (over: Partial<Flow> = {}): Flow => ({
  id: 'f-1', slug: 'recepcao', name: 'Recepção', description: '', type: 'INBOUND', status: 'draft', draft_revision: 1, active_version: null, priority: 100,
  is_default: false, trigger_filter: {}, restart_policy: 'new_conversation_only', source_template_slug: null, source_template_version: null,
  created_at: '2026-10-05T10:00:00Z', updated_at: '2026-10-05T10:00:00Z', ...over,
})

const queue = (id: string, name: string) => ({ id, name, mode: 'manual', is_default: false, member_count: 1, available_count: 1, open_conversation_count: 0, created_at: '', updated_at: '' })

const mapping = (key: string, description: string) => ({ key, kind: 'queue', description, required: true })
const PACK: PackInfo = {
  slug: 'omnira-starter', version: 1, name: 'OMNIRA Starter Pack', description: 'Recepção inteligente e mais.', categories: ['GENERAL'], recommended_for: ['isp'],
  items: [
    { template: 'smart-reception', name: 'Recepção inteligente', type: 'INBOUND', version: 1, optional: false, dependency: false },
    { template: 'csat', name: 'Pesquisa de satisfação', type: 'SUBFLOW', version: 1, optional: true, dependency: false },
    { template: 'unknown-contact', name: 'Contato desconhecido', type: 'SUBFLOW', version: 1, optional: false, dependency: true },
  ],
  mappings: [mapping('queue.technical', 'Fila do suporte técnico'), mapping('queue.finance', 'Fila do financeiro')],
  required_features: ['conversations'], optional_features: ['ticketing'],
}
const TEMPLATE: TemplateInfo = {
  slug: 'isp-link-down', version: 1, name: 'ISP - Link fora', description: 'Coleta circuito e abre o chamado.', type: 'SUBFLOW', categories: ['ISP', 'NOC'], difficulty: 'intermediate',
  recommended_for: ['isp'], required_features: ['conversations'], optional_features: ['monitoring'], mappings: [mapping('queue.noc', 'Fila do NOC')], requires_subflows: [], test_cases: 5, hash: 'h',
}
const RUN: RunSummary = { id: 'r-1', flow_id: 'f-1', flow_slug: 'recepcao', flow_name: 'Recepção', flow_version: 2, conversation_id: 'c-1', status: 'waiting_human', node_executions: 6, started_at: '2026-10-05T10:00:00Z', completed_at: null, duration_ms: null }
const RUN_DETAIL: RunDetail = {
  ...RUN, variables: { assunto: 'Suporte', _state: 'hidden-by-backend' } as any, handoff: { summary: 'Cliente ACME: link caiu' },
  timeline: [
    { seq: 1, node_id: 'start', node_type: 'trigger', status: 'completed', port: 'next', started_at: '2026-10-05T10:00:00Z', duration_ms: 2 },
    { seq: 2, node_id: 'h', node_type: 'human_handoff', status: 'completed', started_at: '2026-10-05T10:00:01Z', duration_ms: 1500 },
  ],
}

interface Server {
  permissions: string[]
  flows?: Flow[]
  queues?: ReturnType<typeof queue>[]
  runs?: RunSummary[]
  analyticsRuns?: number
}

function serve(s: Server) {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: s.permissions } }
    if (/\/flows\/[^/]+\/analytics$/.test(url)) return { data: { days: 7, runs: s.analyticsRuns ?? 0, by_status: {}, completed: 0, failed: 0, human_handoffs: 0, avg_duration_ms: null, node_errors: [], drop_off_by_node: [] } }
    if (url.endsWith('/flow-runs/r-1')) return { data: RUN_DETAIL }
    if (url.endsWith('/flow-runs')) return { data: { items: s.runs ?? [] } }
    if (url.endsWith('/flow-packs')) return { data: { items: [PACK] } }
    if (/\/flow-packs\/omnira-starter$/.test(url)) {
      const picked: string[] | undefined = config?.params?.templates?.split(',')
      const items = picked ? PACK.items.filter((i) => picked.includes(i.template) || i.dependency) : PACK.items
      return { data: { ...PACK, items } }
    }
    if (url.endsWith('/flow-templates')) return { data: { items: [TEMPLATE] } }
    if (url.endsWith('/flow-templates/isp-link-down')) return { data: { ...TEMPLATE, definition: { schema_version: 1, nodes: [{ id: 'start', type: 'trigger', position: { x: 0, y: 0 } }], edges: [], variables: [], settings: {} } } }
    if (url.endsWith('/queues')) return { data: { items: s.queues ?? [] } }
    if (url.endsWith('/flows')) return { data: { items: s.flows ?? [] } }
    return Promise.reject({ response: { status: 404, data: {} } })
  })
}

const ALL = ['flow.view', 'flow.create', 'flow_template.view', 'flow_template.install', 'flow_run.view']
const render = () => renderAt(<FlowsPage />, '/flows')

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('FlowsPage access', () => {
  it('refuses a user with none of the flow permissions', async () => {
    serve({ permissions: ['ticket.read'] })
    render()
    expect(await screen.findByText(/não tem permissão para ver a automação/i)).toBeInTheDocument()
  })

  it('enables each tab only with the permission the backend asks for on that area', async () => {
    serve({ permissions: ['flow.view'] })
    render()
    expect(await screen.findByRole('tab', { name: 'Fluxos' })).toBeEnabled()
    expect(screen.getByRole('tab', { name: 'Modelos e packs' })).toBeDisabled()
    expect(screen.getByRole('tab', { name: 'Execuções' })).toBeDisabled()
    expect(screen.queryByRole('button', { name: 'Novo fluxo' })).toBeNull()
  })
})

describe('Flow list and creation', () => {
  it('lists flows with their situation, default marker and template origin', async () => {
    serve({ permissions: ALL, flows: [flow({ status: 'published', active_version: 3, is_default: true, source_template_slug: 'smart-reception' }), flow({ id: 'f-2', name: 'Suporte VPN', type: 'SUBFLOW' })] })
    render()
    expect(await screen.findByText('Publicado v3')).toBeInTheDocument()
    expect(screen.getByText('padrão')).toBeInTheDocument()
    expect(screen.getByText('de smart-reception')).toBeInTheDocument()
    expect(screen.getByText('Rascunho')).toBeInTheDocument()
    expect(screen.getByText('Subfluxo')).toBeInTheDocument()
  })

  it('shows an honest empty state', async () => {
    serve({ permissions: ALL, flows: [] })
    render()
    expect(await screen.findByText('Nenhum fluxo ainda')).toBeInTheDocument()
  })

  it('creates a flow with an identifier derived from the name and opens the editor', async () => {
    serve({ permissions: ALL })
    vi.mocked(axios.post).mockResolvedValue({ data: flow({ id: 'new-1' }) })
    const user = userEvent.setup()
    renderAt(<FlowsPage />, '/flows', '/flows')
    await user.click(await screen.findByRole('button', { name: 'Novo fluxo' }))
    await user.type(screen.getByLabelText('Nome'), 'Atendimento Técnico')
    expect(screen.getByLabelText('Identificador')).toHaveValue('atendimento-tecnico')
    await user.click(screen.getByRole('button', { name: 'Criar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledWith(expect.stringMatching(/\/flows$/), { slug: 'atendimento-tecnico', name: 'Atendimento Técnico', type: 'INBOUND' }, expect.anything()))
    expect(await screen.findByTestId('elsewhere')).toBeInTheDocument() // navigated to /flows/new-1
  })

  it('keeps the typed identifier once the person edits it and shows the backend reason on a duplicate', async () => {
    serve({ permissions: ALL })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 409, data: { error: 'slug_taken' } } })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('button', { name: 'Novo fluxo' }))
    await user.type(screen.getByLabelText('Nome'), 'Recepção')
    await user.clear(screen.getByLabelText('Identificador'))
    await user.type(screen.getByLabelText('Identificador'), 'meu-fluxo')
    await user.type(screen.getByLabelText('Nome'), ' 2')
    expect(screen.getByLabelText('Identificador')).toHaveValue('meu-fluxo')
    await user.click(screen.getByRole('button', { name: 'Criar' }))
    expect(await screen.findByText('Já existe um fluxo com este identificador.')).toBeInTheDocument()
  })
})

describe('Template library and install wizard', () => {
  async function openLibrary(user: ReturnType<typeof userEvent.setup>) {
    await user.click(await screen.findByRole('tab', { name: 'Modelos e packs' }))
  }

  it('shows packs and templates, and hides installation from a viewer', async () => {
    serve({ permissions: ['flow.view', 'flow_template.view'] })
    const user = userEvent.setup()
    render()
    await openLibrary(user)
    expect(await screen.findByText('OMNIRA Starter Pack')).toBeInTheDocument()
    expect(screen.getByText('ISP - Link fora')).toBeInTheDocument()
    expect(screen.getByText(/Você não tem permissão para instalar/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Instalar pack' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Ver detalhes' }))
    expect(await screen.findByText(/5 cenário\(s\) de teste/)).toBeInTheDocument()
    expect(screen.getByText('Fila do NOC')).toBeInTheDocument()
  })

  it('walks choose -> map -> confirm -> install and reports drafts only', async () => {
    serve({ permissions: ALL, queues: [queue('q-tec', 'Técnico'), queue('q-fin', 'Financeiro')] })
    vi.mocked(axios.post).mockResolvedValue({ data: { pack_installation_id: 'p-1', flows: [
      { flow_id: 'n-1', slug: 'smart-reception', template: 'smart-reception', template_version: 1, is_default: true, dependency: false },
      { flow_id: 'n-2', slug: 'unknown-contact', template: 'unknown-contact', template_version: 1, is_default: false, dependency: true },
    ], notes: [] } })
    const user = userEvent.setup()
    render()
    await openLibrary(user)
    await user.click(await screen.findByRole('button', { name: 'Instalar pack' }))
    const dialog = await screen.findByRole('dialog', { name: /Instalar OMNIRA Starter Pack/ })
    expect(within(dialog).getByText(/Nada é publicado/)).toBeInTheDocument()
    expect(within(dialog).getByRole('list', { name: 'Etapas' })).toHaveTextContent('1Seleção2Filas3Confirmação')
    expect(within(dialog).getByText('Seleção').closest('li')).toHaveAttribute('aria-current', 'step')
    // choose: non-optional preselected, optional not, dependency announced rather than offered
    expect(within(dialog).getByLabelText(/Recepção inteligente/)).toBeChecked()
    expect(within(dialog).getByLabelText(/Pesquisa de satisfação/)).not.toBeChecked()
    expect(within(dialog).getByText(/Também serão instalados.*Contato desconhecido/)).toBeInTheDocument()
    await user.click(within(dialog).getByLabelText(/Pesquisa de satisfação/))
    await user.click(within(dialog).getByRole('button', { name: 'Continuar' }))
    // map: cannot continue until every queue is chosen
    const next = within(dialog).getByRole('button', { name: 'Continuar' })
    expect(next).toBeDisabled()
    await user.selectOptions(within(dialog).getByLabelText('Fila do suporte técnico'), 'q-tec')
    expect(next).toBeDisabled()
    await user.selectOptions(within(dialog).getByLabelText('Fila do financeiro'), 'q-fin')
    expect(next).toBeEnabled()
    await user.click(next)
    // confirm shows the human names of the queues, then installs
    expect(within(dialog).getByText('Técnico')).toBeInTheDocument()
    await user.click(within(dialog).getByRole('button', { name: 'Instalar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalled())
    const [url, body] = vi.mocked(axios.post).mock.calls[0] as [string, any]
    expect(url).toMatch(/\/flow-packs\/omnira-starter\/install$/)
    expect(body.mappings).toEqual({ 'queue.technical': 'q-tec', 'queue.finance': 'q-fin' })
    expect(body.templates.sort()).toEqual(['csat', 'smart-reception'])
    const done = await screen.findByTestId('install-result')
    expect(within(done).getByText(/2 fluxo\(s\) criado\(s\) como rascunho/)).toBeInTheDocument()
    expect(within(done).getAllByRole('link', { name: 'Abrir' })[0]).toHaveAttribute('href', '/flows/n-1')
    expect(within(done).getByText(/publique primeiro os subfluxos/)).toBeInTheDocument()
  })

  it('preselects the only queue the tenant has, and warns when there are none', async () => {
    serve({ permissions: ALL, queues: [queue('q-unica', 'Atendimento')] })
    const user = userEvent.setup()
    render()
    await openLibrary(user)
    await user.click(await screen.findByRole('button', { name: 'Instalar' })) // the single template
    const dialog = await screen.findByRole('dialog', { name: /Instalar ISP - Link fora/ })
    expect(within(dialog).getByLabelText('Fila do NOC')).toHaveValue('q-unica')
    expect(within(dialog).getByRole('button', { name: 'Continuar' })).toBeEnabled()
  })

  it('tells the person to create queues first when there are none', async () => {
    serve({ permissions: ALL, queues: [] })
    const user = userEvent.setup()
    render()
    await openLibrary(user)
    await user.click(await screen.findByRole('button', { name: 'Instalar' }))
    const dialog = await screen.findByRole('dialog')
    expect(within(dialog).getByRole('alert')).toHaveTextContent(/ainda não tem filas/)
    expect(within(dialog).getByRole('button', { name: 'Continuar' })).toBeDisabled()
  })

  it('shows the backend refusal (a mapping the server rejects) and leaves nothing marked as installed', async () => {
    serve({ permissions: ALL, queues: [queue('q-1', 'Fila')] })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 400, data: { error: 'missing_mappings', missing: ['queue.noc'] } } })
    const user = userEvent.setup()
    render()
    await openLibrary(user)
    await user.click(await screen.findByRole('button', { name: 'Instalar' }))
    const dialog = await screen.findByRole('dialog')
    await user.click(within(dialog).getByRole('button', { name: 'Continuar' }))
    await user.click(within(dialog).getByRole('button', { name: 'Instalar' }))
    expect(await within(dialog).findByText('Falta mapear: queue.noc.')).toBeInTheDocument()
    expect(screen.queryByTestId('install-result')).toBeNull()
  })
})

describe('Runs', () => {
  it('lists runs and opens the step-by-step timeline with what was handed to the agent', async () => {
    serve({ permissions: ALL, flows: [flow()], runs: [RUN] })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: 'Execuções' }))
    const table = await screen.findByRole('table')
    expect(within(table).getByText('Com um humano')).toBeInTheDocument()
    await user.click(within(table).getByRole('button', { name: /Recepção v2/ }))
    const detail = await screen.findByTestId('run-detail')
    expect(within(detail).getByText('Cliente ACME: link caiu')).toBeInTheDocument()
    expect(within(detail).getByText(/Transferir para humano/)).toBeInTheDocument()
    expect(within(detail).getByText('Suporte')).toBeInTheDocument() // collected data
    expect(within(detail).queryByText('hidden-by-backend')).toBeNull() // object-valued/private entries never rendered
  })

  it('shows measured analytics only, with an honest note when there is no data', async () => {
    serve({ permissions: ALL, flows: [flow()], runs: [], analyticsRuns: 0 })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: 'Execuções' }))
    await user.selectOptions(await screen.findByLabelText('Filtrar por fluxo'), 'f-1')
    expect(await screen.findByText(/nenhum número é estimado/)).toBeInTheDocument()
    expect(screen.getByText('Duração média').closest('div')).toHaveTextContent('—')
  })
})
