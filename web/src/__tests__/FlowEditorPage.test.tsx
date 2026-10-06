import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import FlowEditorPage from '../pages/FlowEditorPage'
import type { FlowDefinition, FlowDetail, Issue, NodeTypeInfo } from '../lib/flows'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const FLOW_ID = 'f-1'
const emptyDef: FlowDefinition = { schema_version: 1, nodes: [], edges: [], variables: [], settings: {}, metadata: {} }

const flow = (over: Partial<FlowDetail> = {}): FlowDetail => ({
  id: FLOW_ID, slug: 'recepcao', name: 'Recepção', description: '', type: 'INBOUND', status: 'draft', draft_revision: 3, active_version: null,
  priority: 100, is_default: false, trigger_filter: {}, restart_policy: 'new_conversation_only', source_template_slug: null, source_template_version: null,
  created_at: '2026-10-05T10:00:00Z', updated_at: '2026-10-05T10:00:00Z', definition: emptyDef, ...over,
})

const nt = (type: string, category: string, extra: Partial<NodeTypeInfo> = {}): NodeTypeInfo => ({ type, label: type, category, side_effect: 'none', waits: false, terminal: false, ports: [], ...extra })
const NODE_TYPES: NodeTypeInfo[] = [
  nt('trigger', 'flow'), nt('end', 'flow', { terminal: true }), nt('send_message', 'conversation', { side_effect: 'external' }),
  nt('ask', 'conversation', { waits: true }), nt('create_ticket', 'action', { side_effect: 'local' }), nt('ai_classify_intent', 'ai'),
]

interface Server {
  permissions: string[]
  flow?: FlowDetail
  issues?: Issue[]
  versions?: { id: string; version: number; note: string; published_at: string; definition_hash: string; subflow_pins: Record<string, string>; published_by: string | null }[]
}

function serve(s: Server) {
  const detail = s.flow ?? flow()
  vi.mocked(axios.get).mockImplementation(async (url: string) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: s.permissions } }
    if (url.endsWith(`/flows/${FLOW_ID}/versions`)) return { data: { items: s.versions ?? [] } }
    if (url.endsWith(`/flows/${FLOW_ID}`)) return { data: detail }
    if (url.endsWith('/flow-node-types')) return { data: { items: NODE_TYPES } }
    if (url.endsWith('/queues')) return { data: { items: [{ id: 'q-1', name: 'Suporte', mode: 'manual', is_default: true, member_count: 1, available_count: 1, open_conversation_count: 0, created_at: '', updated_at: '' }] } }
    if (url.endsWith('/flows')) return { data: { items: [] } }
    return Promise.reject({ response: { status: 404, data: {} } })
  })
  vi.mocked(axios.post).mockImplementation(async (url: string) => {
    if (url.endsWith('/flows/validate') || url.endsWith(`/flows/${FLOW_ID}/validate`)) return { data: { valid: !(s.issues ?? []).some((i) => i.severity === 'error'), issues: s.issues ?? [] } }
    return Promise.reject({ response: { status: 404, data: {} } })
  })
  vi.mocked(axios.put).mockImplementation(async (_url: string, body: any) => ({ data: { flow: { ...detail, draft_revision: body.revision + 1 }, issues: s.issues ?? [] } }))
}

const ADMIN = ['flow.view', 'flow.create', 'flow.edit', 'flow.test', 'flow.publish', 'flow.archive']
const render = () => renderAt(<FlowEditorPage />, `/flows/${FLOW_ID}`, '/flows/:flowId')

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

async function addNodes(user: ReturnType<typeof userEvent.setup>, ...labels: string[]) {
  for (const l of labels) await user.click(await screen.findByRole('button', { name: new RegExp(`^${l}`) }))
}

describe('FlowEditorPage permissions', () => {
  it('refuses a user without flow.view', async () => {
    serve({ permissions: ['ticket.read'] })
    render()
    expect(await screen.findByText(/não tem permissão para ver os fluxos/i)).toBeInTheDocument()
    expect(axios.get).not.toHaveBeenCalledWith(expect.stringContaining(`/flows/${FLOW_ID}`), expect.anything())
  })

  it('shows a view-only user a read-only editor: no palette, no save, no publish', async () => {
    serve({ permissions: ['flow.view'] })
    render()
    expect(await screen.findByLabelText('Nome do fluxo')).toBeDisabled()
    expect(screen.getByText(/mas não editá-lo/)).toBeInTheDocument()
    expect(screen.getByText(/Paleta indisponível/)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Salvar rascunho' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Publicar' })).toBeNull()
    // a user with flow.view but not flow.edit is validated against the STORED draft, never an unsaved one
    await waitFor(() => expect(axios.post).toHaveBeenCalledWith(expect.stringContaining(`/flows/${FLOW_ID}/validate`), expect.anything(), expect.anything()), { timeout: 6000 })
  })

  it('lets an editor without flow.publish save but not publish', async () => {
    serve({ permissions: ['flow.view', 'flow.edit'] })
    render()
    expect(await screen.findByRole('button', { name: 'Salvar rascunho' })).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Publicar' })).toBeNull()
  })

  it('hides every action on an archived flow', async () => {
    serve({ permissions: ADMIN, flow: flow({ status: 'archived' }) })
    render()
    expect(await screen.findByText(/arquivado: somente leitura/i)).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Salvar rascunho' })).toBeNull()
    expect(screen.queryByRole('button', { name: 'Arquivar' })).toBeNull()
  })
})

describe('FlowEditorPage editing', () => {
  it('builds a graph from the palette, connects ports, saves with the revision it read and then allows publishing', async () => {
    serve({ permissions: ADMIN })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Início', 'Encerrar')
    expect(screen.getByRole('group', { name: /Nó Início/ })).toBeInTheDocument()
    expect(screen.getByRole('group', { name: /Nó Encerrar/ })).toBeInTheDocument()
    // only one start node can exist
    expect(screen.getByRole('button', { name: /^Início/ })).toBeDisabled()
    expect(screen.getByRole('status', { name: '' })).toBeDefined()

    // connect Início.segue -> Encerrar
    await user.click(screen.getByRole('button', { name: /Saída segue de Início/ }))
    expect(screen.getByText(/Escolha o nó de destino/)).toBeInTheDocument()
    await user.click(screen.getByRole('button', { name: /Entrada de Encerrar/ }))
    expect(screen.getByRole('button', { name: /Remover ligação de Início/ })).toBeInTheDocument()

    // unsaved: publishing is off, saving is on
    expect(screen.getByText('Alterações não salvas')).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Publicar' })).toBeDisabled()
    await user.click(screen.getByRole('button', { name: 'Salvar rascunho' }))
    await waitFor(() => expect(axios.put).toHaveBeenCalledTimes(1))
    const [url, body] = vi.mocked(axios.put).mock.calls[0] as [string, any]
    expect(url).toMatch(new RegExp(`/flows/${FLOW_ID}/draft$`))
    expect(body.revision).toBe(3)
    expect(body.definition.nodes.map((n: any) => n.type)).toEqual(['trigger', 'end'])
    expect(body.definition.edges).toEqual([expect.objectContaining({ source: 'trigger', sourcePort: 'next', target: 'end' })])
    await waitFor(() => expect(screen.getByRole('button', { name: 'Publicar' })).toBeEnabled(), { timeout: 6000 })
  })

  it('explains why a link is refused instead of silently ignoring it', async () => {
    serve({ permissions: ADMIN })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Início', 'Enviar mensagem')
    // an arrow INTO the start node is not allowed: its input handle does not even exist
    expect(screen.queryByRole('button', { name: /Entrada de Início/ })).toBeNull()
    // loop: send_message -> ... cannot point back to itself
    await user.click(screen.getByRole('button', { name: /Saída segue de Enviar mensagem/ }))
    expect(screen.getByRole('button', { name: /Entrada de Enviar mensagem/ })).toBeDisabled()
  })

  it('edits node properties and keeps the graph coherent', async () => {
    serve({ permissions: ADMIN })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Início', 'Enviar mensagem')
    await user.type(await screen.findByLabelText('Texto da mensagem'), 'Olá')
    await user.click(screen.getByRole('button', { name: 'Salvar rascunho' }))
    await waitFor(() => expect(axios.put).toHaveBeenCalled())
    const body = vi.mocked(axios.put).mock.calls[0][1] as any
    expect(body.definition.nodes.find((n: any) => n.type === 'send_message').config.text).toBe('Olá')
  })

  it('offers the ticket priority as a choice of the author, never of the AI', async () => {
    serve({ permissions: ADMIN })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Abrir chamado')
    expect(await screen.findByLabelText('Prioridade')).toHaveValue('medium')
    expect(screen.getByText(/nem o contato nem a IA a definem/)).toBeInTheDocument()
  })

  it('deletes a node together with its links', async () => {
    serve({ permissions: ADMIN })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Início', 'Encerrar')
    await user.click(screen.getByRole('button', { name: /Saída segue de Início/ }))
    await user.click(screen.getByRole('button', { name: /Entrada de Encerrar/ }))
    await user.click(within(screen.getByRole('group', { name: /Nó Encerrar/ })).getByText('Encerrar'))
    await user.click(await screen.findByRole('button', { name: 'Excluir nó' }))
    expect(screen.queryByRole('group', { name: /Nó Encerrar/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Remover ligação/ })).toBeNull()
  })
})

describe('FlowEditorPage validation, conflict and publishing', () => {
  const blocking: Issue[] = [{ severity: 'error', code: 'missing_trigger', message: 'O fluxo não tem nó de início.' }]
  const warning: Issue[] = [{ severity: 'warning', code: 'unreachable_node', node_id: 'end', message: 'Nó inalcançável.' }]

  it('shows the live validation from the backend and blocks publishing on errors', async () => {
    serve({ permissions: ADMIN, flow: flow({ definition: { ...emptyDef, nodes: [{ id: 'end', type: 'end', position: { x: 40, y: 40 } }] } }), issues: blocking })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: /Problemas/ }))
    expect(await screen.findByText('O fluxo não tem nó de início.', {}, { timeout: 6000 })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Publicar' })).toBeDisabled()
  })

  it('lets warnings through and marks the node', async () => {
    serve({ permissions: ADMIN, flow: flow({ definition: { ...emptyDef, nodes: [{ id: 'end', type: 'end', position: { x: 40, y: 40 } }] } }), issues: warning })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: /Problemas/ }))
    expect(await screen.findByText('Nó inalcançável.', {}, { timeout: 6000 })).toBeInTheDocument()
    await waitFor(() => expect(screen.getByRole('button', { name: 'Publicar' })).toBeEnabled(), { timeout: 6000 })
  })

  it('handles an edit conflict (409) by blocking save/publish and offering a reload', async () => {
    serve({ permissions: ADMIN })
    vi.mocked(axios.put).mockRejectedValue({ response: { status: 409, data: { error: 'revision_conflict' } } })
    const user = userEvent.setup()
    render()
    await addNodes(user, 'Início')
    await user.click(screen.getByRole('button', { name: 'Salvar rascunho' }))
    expect(await screen.findByText(/alterado por outra pessoa/)).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Recarregar' })).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Salvar rascunho' })).toBeDisabled()
    expect(screen.getByRole('button', { name: 'Publicar' })).toBeDisabled()
  })

  it('publishes the saved revision with a note and reports the new version', async () => {
    serve({ permissions: ADMIN, flow: flow({ definition: { ...emptyDef, nodes: [{ id: 'trigger', type: 'trigger', position: { x: 40, y: 40 } }, { id: 'end', type: 'end', position: { x: 340, y: 40 } }], edges: [{ id: 'e1', source: 'trigger', sourcePort: 'next', target: 'end' }] } }) })
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (url.endsWith('/validate')) return { data: { valid: true, issues: [] } }
      if (url.endsWith(`/flows/${FLOW_ID}/publish`)) return { data: { version: { id: 'v', version: 4, note: 'go', published_at: '2026-10-06T00:00:00Z', definition_hash: 'h', subflow_pins: {}, published_by: null }, warnings: [] } }
      return Promise.reject({ response: { status: 404, data: {} } })
    })
    const user = userEvent.setup()
    render()
    const publish = await screen.findByRole('button', { name: 'Publicar' })
    await waitFor(() => expect(publish).toBeEnabled(), { timeout: 6000 })
    await user.click(publish)
    await user.type(await screen.findByLabelText(/Nota da versão/), 'go')
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Publicar' }))
    await waitFor(() => expect(vi.mocked(axios.post).mock.calls.map((c) => String(c[0]))).toContainEqual(expect.stringMatching(/\/publish$/)))
    const published = vi.mocked(axios.post).mock.calls.find((c) => String(c[0]).endsWith('/publish')) as [string, any]
    // the whole note must arrive: typing in the modal must not lose focus on every key
    expect(published[1]).toEqual({ revision: 3, note: 'go' })
    expect(await screen.findByText(/Versão 4 publicada/)).toBeInTheDocument()
  })

  it('surfaces the backend issues when the server refuses to publish (422)', async () => {
    serve({ permissions: ADMIN, flow: flow({ definition: { ...emptyDef, nodes: [{ id: 'trigger', type: 'trigger', position: { x: 40, y: 40 } }] } }) })
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (url.endsWith('/validate')) return { data: { valid: true, issues: [] } }
      return Promise.reject({ response: { status: 422, data: { error: 'not_publishable', issues: [{ severity: 'error', code: 'unconnected_port', node_id: 'trigger', message: 'A saída "next" não está ligada.' }] } } })
    })
    const user = userEvent.setup()
    render()
    const publish = await screen.findByRole('button', { name: 'Publicar' })
    await waitFor(() => expect(publish).toBeEnabled(), { timeout: 6000 })
    await user.click(publish)
    await user.click(within(screen.getByRole('dialog')).getByRole('button', { name: 'Publicar' }))
    expect(await screen.findByText('A saída "next" não está ligada.')).toBeInTheDocument()
  })
})

describe('FlowEditorPage simulator and versions', () => {
  it('runs a scenario against the editor definition and shows what the bot would do (never real effects)', async () => {
    serve({ permissions: ADMIN, flow: flow({ definition: { ...emptyDef, nodes: [{ id: 'trigger', type: 'trigger', position: { x: 40, y: 40 } }] } }) })
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (url.endsWith('/validate')) return { data: { valid: true, issues: [] } }
      if (url.endsWith('/simulate')) return { data: { status: 'waiting_human', steps: [{ seq: 1, node_id: 'trigger', node_type: 'trigger', status: 'completed', port: 'next' }], messages: [{ step: 2, text: 'Olá Ana' }], effects: [{ step: 3, kind: 'ticket', detail: { subject: 'VPN', priority: 'high' } }], variables: {}, events_consumed: 1 } }
      return Promise.reject({ response: { status: 404, data: {} } })
    })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: 'Simular' }))
    await user.click(screen.getByRole('button', { name: 'Simular' }))
    const result = await screen.findByTestId('simulation-result')
    expect(within(result).getByText('Olá Ana')).toBeInTheDocument()
    expect(within(result).getByText(/Abriria um chamado: "VPN" \(prioridade high\)/)).toBeInTheDocument()
    expect(within(result).getByText('Entregue a um humano')).toBeInTheDocument()
    const call = vi.mocked(axios.post).mock.calls.find((c) => String(c[0]).endsWith('/simulate')) as [string, any]
    expect(call[1].scenario.events).toEqual([{ type: 'message', text: 'oi' }])
    expect(call[1].definition.nodes).toHaveLength(1) // the unsaved editor definition is what runs
  })

  it('disables simulation for users without flow.test', async () => {
    serve({ permissions: ['flow.view'] })
    render()
    expect(await screen.findByRole('tab', { name: 'Simular' })).toBeDisabled()
  })

  it('lists versions and lets only a publisher roll back', async () => {
    const versions = [
      { id: 'v2', version: 2, note: 'segunda', published_at: '2026-10-05T12:00:00Z', definition_hash: 'b', subflow_pins: {}, published_by: null },
      { id: 'v1', version: 1, note: '', published_at: '2026-10-04T12:00:00Z', definition_hash: 'a', subflow_pins: {}, published_by: null },
    ]
    serve({ permissions: ADMIN, versions, flow: flow({ status: 'published', active_version: 2 }) })
    vi.mocked(axios.post).mockImplementation(async (url: string) => {
      if (url.endsWith('/validate')) return { data: { valid: true, issues: [] } }
      if (url.endsWith('/versions/1/activate')) return { data: flow({ status: 'published', active_version: 1 }) }
      return Promise.reject({ response: { status: 404, data: {} } })
    })
    const user = userEvent.setup()
    render()
    await user.click(await screen.findByRole('tab', { name: 'Versões' }))
    expect(await screen.findByText('ativa')).toBeInTheDocument()
    expect(screen.queryByRole('button', { name: 'Ativar a versão 2' })).toBeNull()
    await user.click(screen.getByRole('button', { name: 'Ativar a versão 1' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledWith(expect.stringMatching(/\/versions\/1\/activate$/), {}, expect.anything()))
    expect(await screen.findByText(/Versão reativada/)).toBeInTheDocument()
  })
})
