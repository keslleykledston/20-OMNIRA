import { beforeEach, describe, expect, it, vi } from 'vitest'
import { screen, waitFor, within } from '@testing-library/react'
import userEvent from '@testing-library/user-event'
import axios from 'axios'
import GroupsPage from '../pages/GroupsPage'
import type { GroupImport, MemoryItem, MemoryList, Proposal } from '../lib/groupAi'
import type { Group } from '../lib/groups'
import { renderAt, setSession } from './testUtils'

vi.mock('axios')

const READ_ALL = ['group.read', 'group.ai.read']
const MANAGE_ALL = ['group.read', 'group.manage', 'group.ai.read', 'group.ai.manage', 'group.import', 'group.memory.erase']

const group: Group = {
  id: 'g-1',
  name: 'Cobrança K3G',
  enabled: true,
  purpose: 'client_support',
  account_id: null,
  account_name: null,
  additional_accounts: [],
  connection_id: 'c-1',
  connection_account: 'K3G',
  ai_observer_enabled: false,
  is_alert_channel: false,
  last_message_at: null,
  last_message: null,
}

const item = (over: Partial<MemoryItem> = {}): MemoryItem => ({
  id: 'i-1',
  kind: 'request',
  tier: 'episodic',
  status: 'open',
  truth_level: 'ai_inferred',
  summary: 'Cliente pediu a segunda via do boleto',
  actor_label: 'Rafael',
  evidence: [{ quote: 'preciso da segunda via do boleto', at: '2026-11-01T10:00:00Z' }],
  pinned: false,
  relevance: 1,
  last_reinforced_at: '2026-11-01T10:00:00Z',
  source: 'live',
  version: 3,
  created_at: '2026-11-01T10:00:00Z',
  updated_at: '2026-11-01T10:00:00Z',
  ...over,
})
const imp = (over: Partial<GroupImport> = {}): GroupImport => ({
  id: 'imp-1',
  status: 'extracted',
  format: 'whatsapp_txt',
  source_filename: 'Conversa antiga.txt',
  sha256: 'x',
  size_bytes: 2048,
  messages_total: 100,
  messages_parsed: 90,
  messages_rejected: 6,
  messages_duplicate: 4,
  rejected_reasons: { system: 6 },
  batches_total: 3,
  batches_ok: 3,
  batches_failed: 0,
  items_created: 12,
  coverage_complete: true,
  created_at: '2026-11-01T10:00:00Z',
  expires_at: '2026-01-30T10:00:00Z',
  raw_available: true,
  ...over,
})
const proposal = (over: Partial<Proposal> = {}): Proposal => ({
  id: 'p-1',
  memory_item_id: 'i-1',
  kind: 'case',
  title: 'Segunda via do boleto',
  description: 'Enviar o boleto ao cliente',
  priority: 'normal',
  proposed_by: 'ai',
  status: 'pending_approval',
  version: 2,
  created_at: '2026-11-01T10:00:00Z',
  ...over,
})

interface Server {
  permissions: string[]
  memory?: Partial<MemoryList> | number // a number = the HTTP status the probe answers
  imports?: GroupImport[] | number
  proposals?: Proposal[] | number
  search?: MemoryItem[]
}

function serve(s: Server) {
  vi.mocked(axios.get).mockImplementation(async (url: string, config?: any) => {
    if (url.endsWith('/me/access')) return { data: { role_key: 'x', permissions: s.permissions } }
    if (url.endsWith('/ai/memory/search')) return { data: { items: s.search ?? [], count: (s.search ?? []).length } }
    if (url.endsWith('/ai/memory')) {
      if (typeof s.memory === 'number') return Promise.reject({ response: { status: s.memory } })
      const m = s.memory ?? {}
      return { data: { group: { id: 'g-1', name: group.name, enabled: true, ai_enabled: true }, items: [item()], count: 1, state: {}, ...m } }
    }
    if (url.endsWith('/ai/imports')) {
      if (typeof s.imports === 'number') return Promise.reject({ response: { status: s.imports } })
      return { data: { items: s.imports ?? [], count: (s.imports ?? []).length } }
    }
    if (url.endsWith('/ai/proposals')) {
      if (typeof s.proposals === 'number') return Promise.reject({ response: { status: s.proposals } })
      const all = s.proposals ?? []
      const items = config?.params?.status ? all.filter((p) => p.status === config.params.status) : all
      return { data: { items, count: items.length } }
    }
    if (/\/groups\/[^/]+\/messages$/.test(url)) return { data: { items: [], has_more: false } }
    if (url.endsWith('/groups')) return { data: { items: [group] } }
    return Promise.reject({ response: { status: 404 } })
  })
}

async function openCopilot(user: ReturnType<typeof userEvent.setup>, tab?: string) {
  renderAt(<GroupsPage />)
  await user.click(await screen.findByRole('button', { name: /Cobrança K3G/ }))
  await user.click(await screen.findByRole('button', { name: /^Copiloto/ }))
  const panel = await screen.findByRole('dialog', { name: /Copiloto do grupo/ })
  if (tab) await user.click(await within(panel).findByRole('tab', { name: new RegExp(tab) }))
  return panel
}

const memoryCalls = () => vi.mocked(axios.get).mock.calls.filter(([u]) => (u as string).endsWith('/ai/memory')).length

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('feature detection', () => {
  it('shows no Copiloto and no error when the backend flag is off (404)', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, memory: 404 })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /Cobrança K3G/ }))
    await screen.findByText(/Somente leitura/)
    await waitFor(() => expect(memoryCalls()).toBe(1))
    expect(screen.queryByRole('button', { name: /Copiloto/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Apagar histórico' })).toBeInTheDocument() // the rest of the header is untouched
  })

  it('does not even ask without group.ai.read', async () => {
    const user = userEvent.setup()
    serve({ permissions: ['group.read'] })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /Cobrança K3G/ }))
    await screen.findByText(/Somente leitura/)
    expect(memoryCalls()).toBe(0)
    expect(screen.queryByRole('button', { name: /Copiloto/ })).not.toBeInTheDocument()
  })

  it('hides itself when the server refuses the read (403), also without an error', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, memory: 403 })
    renderAt(<GroupsPage />)
    await user.click(await screen.findByRole('button', { name: /Cobrança K3G/ }))
    await waitFor(() => expect(memoryCalls()).toBe(1))
    expect(screen.queryByRole('button', { name: /Copiloto/ })).not.toBeInTheDocument()
    expect(screen.queryByRole('alert')).not.toBeInTheDocument()
  })

  it('appears with the number of open requests, and drops the Importações and Propostas tabs whose routes answer 404', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, imports: 404, proposals: 404 })
    const panel = await openCopilot(user)
    await waitFor(() => expect(within(panel).queryByRole('tab', { name: /Importações/ })).not.toBeInTheDocument())
    expect(within(panel).queryByRole('tab', { name: /Propostas/ })).not.toBeInTheDocument()
    expect(within(panel).getByRole('tab', { name: /Memória/ })).toBeInTheDocument()
    expect(within(panel).getByRole('tab', { name: 'Ajustes' })).toBeInTheDocument()
  })
})

describe('memory', () => {
  it('shows the item as the server sent it, with who, origin, truth badge, and the evidence only on request', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ_ALL,
      memory: {
        items: [item({ pinned: true }), item({ id: 'i-2', kind: 'fact', status: 'resolved', truth_level: 'human_confirmed', summary: 'Contrato vence dia 10', evidence: [], tier: 'consolidated', source: 'consolidation' })],
        count: 2,
      },
    })
    const panel = await openCopilot(user)
    const [first, second] = await within(panel).findAllByRole('listitem')
    expect(within(first).getByText('Cliente pediu a segunda via do boleto')).toBeInTheDocument()
    expect(within(first).getByText('Pedido')).toBeInTheDocument()
    expect(within(first).getByText('Inferido pela IA')).toBeInTheDocument()
    expect(within(first).getByText('Fixado')).toBeInTheDocument()
    expect(within(first).getByText(/Quem: Rafael/)).toBeInTheDocument()
    expect(within(first).getByText(/Origem: mensagens do grupo/)).toBeInTheDocument()
    expect(within(first).queryByText('preciso da segunda via do boleto')).not.toBeInTheDocument()
    await user.click(within(first).getByRole('button', { name: /Ver evidência \(1\)/ }))
    expect(within(first).getByText('preciso da segunda via do boleto')).toBeInTheDocument()

    expect(within(second).getByText('Confirmado por pessoa')).toBeInTheDocument()
    expect(within(second).getByText('Condensado')).toBeInTheDocument()
    expect(within(second).getByText(/não guarda trecho literal/)).toBeInTheDocument() // says so, never invents a quote
  })

  it('says a removed quote was removed', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, memory: { items: [item({ evidence: [{ at: '2026-11-01T10:00:00Z' }] })] } })
    const panel = await openCopilot(user)
    await user.click(await within(panel).findByRole('button', { name: /Ver evidência/ }))
    expect(within(panel).getByText(/Trecho removido/)).toBeInTheDocument()
  })

  it('filters by kind and by status', async () => {
    const user = userEvent.setup()
    serve({
      permissions: READ_ALL,
      memory: { items: [item(), item({ id: 'i-2', kind: 'fact', summary: 'Contrato vence dia 10' }), item({ id: 'i-3', status: 'resolved', summary: 'Pedido já resolvido' })] },
    })
    const panel = await openCopilot(user)
    await within(panel).findByText('Contrato vence dia 10')
    await user.selectOptions(within(panel).getByLabelText('Filtrar por tipo'), 'fact')
    expect(within(panel).queryByText('Cliente pediu a segunda via do boleto')).not.toBeInTheDocument()
    expect(within(panel).getByText('Contrato vence dia 10')).toBeInTheDocument()
    await user.selectOptions(within(panel).getByLabelText('Filtrar por tipo'), '')
    await user.selectOptions(within(panel).getByLabelText('Filtrar por situação'), 'resolved')
    expect(within(panel).getByText('Pedido já resolvido')).toBeInTheDocument()
    expect(within(panel).queryByText('Contrato vence dia 10')).not.toBeInTheDocument()
  })

  it('searches through the search endpoint and shows only its answer', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, search: [item({ id: 'i-9', summary: 'Boleto vence dia 5' })] })
    const panel = await openCopilot(user)
    await within(panel).findByText('Cliente pediu a segunda via do boleto')
    await user.type(within(panel).getByLabelText('Buscar na memória'), 'boleto')
    expect(await within(panel).findByText('Boleto vence dia 5')).toBeInTheDocument()
    expect(within(panel).queryByText('Cliente pediu a segunda via do boleto')).not.toBeInTheDocument()
    const call = vi.mocked(axios.get).mock.calls.find(([u]) => (u as string).endsWith('/ai/memory/search'))!
    expect((call[1] as any).params).toEqual({ q: 'boleto' })
  })

  it('offers no actions to a reader, and none of the buttons that change things', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL })
    const panel = await openCopilot(user)
    await within(panel).findByText('Cliente pediu a segunda via do boleto')
    for (const name of ['Confirmar', 'Fixar', 'Editar', 'Marcar como resolvido', 'Apagar', 'Criar ocorrência', 'Criar tarefa', 'Analisar agora', 'Consolidar agora', 'Registrar item']) {
      expect(within(panel).queryByRole('button', { name })).not.toBeInTheDocument()
    }
  })

  it('confirms, pins and resolves with the version the screen read', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.patch).mockResolvedValue({ data: item() })
    const panel = await openCopilot(user)
    const card = (await within(panel).findAllByRole('listitem'))[0]
    await user.click(within(card).getByRole('button', { name: 'Confirmar' }))
    await user.click(within(card).getByRole('button', { name: 'Fixar' }))
    await user.click(within(card).getByRole('button', { name: 'Marcar como resolvido' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(3))
    expect(vi.mocked(axios.patch).mock.calls.map((c) => c[1])).toEqual([
      { confirm: true, version: 3 },
      { pinned: true, version: 3 },
      { status: 'resolved', version: 3 },
    ])
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/groups\/g-1\/ai\/memory\/i-1$/)
  })

  it('edits the text; on 409 explains, reloads the list and keeps what the person typed', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.patch).mockRejectedValue({ response: { status: 409 } })
    const panel = await openCopilot(user)
    const card = (await within(panel).findAllByRole('listitem'))[0]
    const before = memoryCalls()
    await user.click(within(card).getByRole('button', { name: 'Editar' }))
    const box = within(card).getByLabelText('Texto do item')
    await user.clear(box)
    await user.type(box, 'Cliente pediu o boleto de novembro')
    await user.click(within(card).getByRole('button', { name: 'Salvar' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ summary: 'Cliente pediu o boleto de novembro', version: 3 })
    expect(await screen.findByText(/alterado por outra pessoa/)).toBeInTheDocument()
    await waitFor(() => expect(memoryCalls()).toBeGreaterThan(before)) // reloaded
    expect(within(card).getByLabelText('Texto do item')).toHaveValue('Cliente pediu o boleto de novembro') // the editor stays open
  })

  it('deletes only after confirming', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.delete).mockResolvedValue({ data: undefined, status: 204 })
    const panel = await openCopilot(user)
    const card = (await within(panel).findAllByRole('listitem'))[0]
    await user.click(within(card).getByRole('button', { name: 'Apagar' }))
    expect(axios.delete).not.toHaveBeenCalled()
    const dialog = (await screen.findByText('Apagar este item da memória?')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: 'Apagar item' }))
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/ai\/memory\/i-1$/)
  })

  it('Esc inside a confirmation closes only the confirmation, not the whole panel', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    const panel = await openCopilot(user)
    const card = (await within(panel).findAllByRole('listitem'))[0]
    await user.click(within(card).getByRole('button', { name: 'Apagar' }))
    await screen.findByText('Apagar este item da memória?')
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByText('Apagar este item da memória?')).not.toBeInTheDocument())
    expect(screen.getByRole('dialog', { name: /Copiloto do grupo/ })).toBeInTheDocument()
    await user.keyboard('{Escape}')
    await waitFor(() => expect(screen.queryByRole('dialog')).not.toBeInTheDocument())
  })

  it('registers an item by hand', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.post).mockResolvedValue({ data: item(), status: 201 })
    const panel = await openCopilot(user)
    await user.click(await within(panel).findByRole('button', { name: 'Registrar item' }))
    const form = within(panel).getByRole('form', { name: 'Registrar item na memória' })
    expect(within(form).getByRole('button', { name: 'Registrar' })).toBeDisabled()
    await user.selectOptions(within(form).getByLabelText('Tipo do item'), 'agreement')
    await user.type(within(form).getByLabelText('Resumo do item'), 'Reunião toda segunda às 9h')
    await user.type(within(form).getByLabelText('Quem (opcional)'), 'Gestor')
    await user.click(within(form).getByRole('button', { name: 'Registrar' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ kind: 'agreement', summary: 'Reunião toda segunda às 9h', actor_label: 'Gestor' })
  })

  it('asks for a reading in the background and says it is asynchronous', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], memory: { state: { last_run_at: '2026-11-01T10:00:00Z', last_run_ok: true, runs: 2 } } })
    vi.mocked(axios.post).mockResolvedValue({ data: { requested: true }, status: 202 })
    const panel = await openCopilot(user)
    expect(await within(panel).findByText(/Última leitura em/)).toBeInTheDocument()
    expect(within(panel).getByText(/2 leituras feitas/)).toBeInTheDocument()
    await user.click(within(panel).getByRole('button', { name: 'Analisar agora' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/groups\/g-1\/ai\/analyze$/)
  })

  it('shows the waiting state of a requested reading and blocks asking twice', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], memory: { state: { last_run_at: '2026-11-01T10:00:00Z', analyze_requested_at: '2026-11-02T10:00:00Z' } } })
    const panel = await openCopilot(user)
    expect(await within(panel).findByText(/Pedido de leitura enviado em/)).toBeInTheDocument()
    expect(within(panel).getByText(/processo em segundo plano/)).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: 'Analisar agora' })).toBeDisabled()
  })

  it('with the AI off, says so, keeps the memory readable and disables the actions that need the AI', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], memory: { group: { ai_enabled: false } } })
    const panel = await openCopilot(user)
    expect(await within(panel).findByText(/A IA está desligada neste grupo/)).toBeInTheDocument()
    expect(within(panel).getByText('Cliente pediu a segunda via do boleto')).toBeInTheDocument()
    expect(within(panel).getByRole('button', { name: 'Analisar agora' })).toBeDisabled()
    expect(within(panel).getByRole('button', { name: 'Consolidar agora' })).toBeDisabled()
    expect(within(panel).getByRole('button', { name: 'Registrar item' })).toBeDisabled()
  })

  it('drafts a case or a task from an item, nothing is created, and moves to Propostas', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.post).mockResolvedValue({ data: proposal(), status: 201 })
    const panel = await openCopilot(user)
    const card = (await within(panel).findAllByRole('listitem'))[0]
    await user.click(within(card).getByRole('button', { name: 'Criar tarefa' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/ai\/proposals$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ memory_item_id: 'i-1', kind: 'task' })
    await waitFor(() => expect(within(panel).getByRole('tab', { name: /Propostas/, selected: true })).toBeInTheDocument())
  })

  it('hides "Criar ocorrência/tarefa" when the proposals route does not exist', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: 404 })
    const panel = await openCopilot(user)
    await within(panel).findAllByRole('listitem')
    await waitFor(() => expect(within(panel).queryByRole('tab', { name: /Propostas/ })).not.toBeInTheDocument())
    expect(within(panel).queryByRole('button', { name: 'Criar ocorrência' })).not.toBeInTheDocument()
  })
})

describe('imports', () => {
  it('shows the coverage as "everything was read", not "everything was captured", and disables discarding until the coverage is complete', async () => {
    const user = userEvent.setup()
    serve({
      permissions: MANAGE_ALL,
      proposals: [],
      imports: [
        imp(),
        imp({ id: 'imp-2', source_filename: 'Parcial.zip', format: 'whatsapp_zip', coverage_complete: false, batches_ok: 2, batches_failed: 1, rejected_reasons: undefined }),
      ],
    })
    const panel = await openCopilot(user, 'Importações')
    const [complete, partial] = await within(panel).findAllByRole('listitem', { name: undefined }).then((all) => all.filter((li) => li.textContent?.includes('Registro de cobertura')))

    expect(within(complete).getByText(/Todas as mensagens foram lidas/)).toBeInTheDocument()
    expect(within(complete).getByText(/não que tudo foi registrado/)).toBeInTheDocument()
    expect(complete.textContent).not.toMatch(/capturad/i)
    expect(within(complete).getByText('Conversa antiga.txt')).toBeInTheDocument()
    expect(within(complete).getByText(/Rejeitadas: 6 linhas do sistema/)).toBeInTheDocument()
    expect(within(complete).getByText(/O total inclui as linhas do sistema/)).toBeInTheDocument()
    expect(within(complete).getByRole('button', { name: 'Descartar arquivo original' })).toBeEnabled()

    expect(within(partial).getByText(/Cobertura incompleta/)).toBeInTheDocument()
    expect(within(partial).getByText(/1 lote falhou/)).toBeInTheDocument()
    expect(within(partial).getByRole('button', { name: 'Descartar arquivo original' })).toBeDisabled()
    expect(within(partial).getByText(/descartado sozinho em/)).toBeInTheDocument()
  })

  it('discards the original after a confirmation', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [imp()] })
    vi.mocked(axios.post).mockResolvedValue({ data: imp({ raw_available: false, status: 'discarded' }) })
    const panel = await openCopilot(user, 'Importações')
    await user.click(await within(panel).findByRole('button', { name: 'Descartar arquivo original' }))
    expect(axios.post).not.toHaveBeenCalled()
    const dialog = (await screen.findByText('Descartar o arquivo original?')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: 'Descartar arquivo original' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/ai\/imports\/imp-1\/discard$/)
  })

  it('says why and when an already discarded original is gone, and offers no action on it', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [imp({ raw_available: false, status: 'discarded', discarded_at: '2026-11-05T10:00:00Z', discard_reason: 'expired' })] })
    const panel = await openCopilot(user, 'Importações')
    expect(await within(panel).findByText(/Arquivo original descartado em .*: prazo vencido/)).toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Descartar arquivo original' })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Reprocessar' })).not.toBeInTheDocument()
  })

  it('reprocesses, and explains a refusal (409)', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [imp()] })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 409 } })
    const panel = await openCopilot(user, 'Importações')
    await user.click(await within(panel).findByRole('button', { name: 'Reprocessar' }))
    expect(await within(panel).findByText(/já foi lido por completo com a versão atual/)).toBeInTheDocument()
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/imports\/imp-1\/reprocess$/)
  })

  it('uploads a .txt as the raw body and refuses another extension on the screen (a browser may let "all files" through)', async () => {
    const user = userEvent.setup({ applyAccept: false })
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [] })
    vi.mocked(axios.post).mockResolvedValue({ data: imp({ status: 'uploaded' }), status: 202 })
    const panel = await openCopilot(user, 'Importações')
    const input = await within(panel).findByLabelText('Arquivo da conversa (.txt ou .zip)')
    await user.upload(input, new File(['x'], 'foto.png', { type: 'image/png' }))
    expect(await within(panel).findByText(/um arquivo \.txt ou \.zip/)).toBeInTheDocument()
    expect(axios.post).not.toHaveBeenCalled()
    const file = new File(['[01/11/2026 10:00] Rafael: oi'], 'Conversa.txt', { type: 'text/plain' })
    await user.upload(input, file)
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    const [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, any, any]
    expect(url).toMatch(/\/ai\/imports$/)
    expect(body).toBe(file)
    expect(cfg.params).toEqual({ filename: 'Conversa.txt' })
  })

  it('a reader sees the list but no upload, discard or reprocess', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, proposals: [], imports: [imp()] })
    const panel = await openCopilot(user, 'Importações')
    expect(await within(panel).findByText('Conversa antiga.txt')).toBeInTheDocument()
    expect(within(panel).queryByLabelText('Arquivo da conversa (.txt ou .zip)')).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Descartar arquivo original' })).not.toBeInTheDocument()
    expect(within(panel).queryByRole('button', { name: 'Reprocessar' })).not.toBeInTheDocument()
  })
})

describe('proposals', () => {
  it('shows the draft, edits it, and only approves after the edit is saved', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [proposal()] })
    vi.mocked(axios.patch).mockResolvedValue({ data: proposal({ title: 'Novo título', version: 3 }) })
    const panel = await openCopilot(user, 'Propostas')
    const card = await within(panel).findByRole('listitem', { name: /Ocorrência: Segunda via do boleto/ })
    expect(within(card).getByText('Aguardando aprovação')).toBeInTheDocument()
    expect(within(card).getByText(/Rascunho da IA/)).toBeInTheDocument()
    const approve = within(card).getByRole('button', { name: 'Aprovar e criar ocorrência' })
    expect(approve).toBeEnabled()
    const title = within(card).getByLabelText('Título da proposta')
    await user.clear(title)
    await user.type(title, 'Novo título')
    expect(approve).toBeDisabled()
    expect(within(card).getByText(/Salve as alterações antes de aprovar/)).toBeInTheDocument()
    await user.selectOptions(within(card).getByLabelText('Prioridade'), 'urgent')
    await user.click(within(card).getByRole('button', { name: 'Salvar alterações' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ version: 2, title: 'Novo título', priority: 'urgent' })
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/ai\/proposals\/p-1$/)
  })

  it('approves and creates; the created case id is shown as the confirmation', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [proposal({ status: 'created', created_case_id: 'abcdef12-0000-0000-0000-000000000000', id: 'p-2', title: 'Já criada' })] })
    const panel = await openCopilot(user, 'Propostas')
    const card = await within(panel).findByRole('listitem', { name: /Já criada/ })
    expect(within(card).getByText('Criada')).toBeInTheDocument()
    expect(within(card).getByText('Ocorrência criada (registro abcdef12).')).toBeInTheDocument()
    expect(within(card).queryByRole('button', { name: /Aprovar/ })).not.toBeInTheDocument()
    expect(within(card).queryByLabelText('Título da proposta')).not.toBeInTheDocument() // no longer editable
  })

  it('calls approve, and on 403 names the missing service permission and keeps the proposal pending', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [proposal()] })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 403 } })
    const panel = await openCopilot(user, 'Propostas')
    await user.click(await within(panel).findByRole('button', { name: 'Aprovar e criar ocorrência' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/ai\/proposals\/p-1\/approve$/)
    const alert = await within(panel).findByRole('alert')
    expect(alert).toHaveTextContent(/não tem permissão para criar ocorrências \(service\.create\)/)
    expect(alert).toHaveTextContent(/continua aguardando aprovação/)
    const card = within(panel).getByRole('listitem', { name: /Segunda via do boleto/ })
    expect(within(card).getByText('Aguardando aprovação')).toBeInTheDocument()
  })

  it('on approve 409 (rejected meanwhile) explains and reloads', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [proposal()] })
    vi.mocked(axios.post).mockRejectedValue({ response: { status: 409 } })
    const panel = await openCopilot(user, 'Propostas')
    await user.click(await within(panel).findByRole('button', { name: 'Aprovar e criar ocorrência' }))
    expect(await within(panel).findByText(/já foi rejeitada ou mudou/)).toBeInTheDocument()
  })

  it('rejects with an optional reason', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [proposal({ kind: 'task' })] })
    vi.mocked(axios.post).mockResolvedValue({ data: proposal({ status: 'rejected' }) })
    const panel = await openCopilot(user, 'Propostas')
    await user.click(await within(panel).findByRole('button', { name: 'Rejeitar' }))
    await user.type(within(panel).getByLabelText('Motivo da rejeição (opcional)'), 'Já resolvido por telefone')
    await user.click(within(panel).getByRole('button', { name: 'Confirmar rejeição' }))
    await waitFor(() => expect(axios.post).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.post).mock.calls[0][0]).toMatch(/\/ai\/proposals\/p-1\/reject$/)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ reason: 'Já resolvido por telefone' })
  })

  it('a reader sees the proposals read-only', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, proposals: [proposal()] })
    const panel = await openCopilot(user, 'Propostas')
    const card = await within(panel).findByRole('listitem', { name: /Segunda via do boleto/ })
    expect(within(card).getByText('Enviar o boleto ao cliente')).toBeInTheDocument()
    expect(within(card).queryByRole('button')).not.toBeInTheDocument()
    expect(within(card).queryByLabelText('Título da proposta')).not.toBeInTheDocument()
  })
})

describe('settings: opt-in, privacy and danger zone', () => {
  it('turning the AI on is confirmed, and the screen says excerpts go to the AI provider', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], memory: { group: { ai_enabled: false } } })
    vi.mocked(axios.patch).mockResolvedValue({ data: { id: 'g-1', ai_enabled: true, imports_discarded: 0 } })
    const panel = await openCopilot(user, 'Ajustes')
    expect(await within(panel).findByText(/trechos das mensagens deste grupo são enviados ao provedor de IA/)).toBeInTheDocument()
    const toggle = within(panel).getByRole('switch', { name: 'IA neste grupo' })
    expect(toggle).toHaveAttribute('aria-checked', 'false')
    await user.click(toggle)
    expect(axios.patch).not.toHaveBeenCalled()
    const dialog = (await screen.findByText('Ligar a IA neste grupo?')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: 'Ligar a IA' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][0]).toMatch(/\/groups\/g-1\/ai$/)
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ enabled: true })
  })

  it('turning the AI off asks whether to discard the imported originals too', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [imp()] })
    vi.mocked(axios.patch).mockResolvedValue({ data: { id: 'g-1', ai_enabled: false, imports_discarded: 1 } })
    const panel = await openCopilot(user, 'Ajustes')
    await user.click(await within(panel).findByRole('switch', { name: 'IA neste grupo' }))
    const dialog = (await screen.findByText('Desligar a IA neste grupo?')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByLabelText(/Descartar também os arquivos originais importados/))
    await user.click(within(dialog).getByRole('button', { name: 'Desligar a IA' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ enabled: false, discard_imports: true })
  })

  it('turning it off without the checkbox keeps the originals', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [], imports: [imp()] })
    vi.mocked(axios.patch).mockResolvedValue({ data: { id: 'g-1', ai_enabled: false, imports_discarded: 0 } })
    const panel = await openCopilot(user, 'Ajustes')
    await user.click(await within(panel).findByRole('switch', { name: 'IA neste grupo' }))
    const dialog = (await screen.findByText('Desligar a IA neste grupo?')).closest('[role="dialog"]') as HTMLElement
    await user.click(within(dialog).getByRole('button', { name: 'Desligar a IA' }))
    await waitFor(() => expect(axios.patch).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.patch).mock.calls[0][1]).toEqual({ enabled: false })
  })

  it('a reader cannot flip the switch and does not see the danger zone', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, proposals: [] })
    const panel = await openCopilot(user, 'Ajustes')
    expect(await within(panel).findByRole('switch', { name: 'IA neste grupo' })).toBeDisabled()
    expect(within(panel).queryByRole('button', { name: 'Apagar toda a memória do grupo' })).not.toBeInTheDocument()
  })

  it('states the privacy facts plainly', async () => {
    const user = userEvent.setup()
    serve({ permissions: READ_ALL, proposals: [] })
    const panel = await openCopilot(user, 'Ajustes')
    expect(await within(panel).findByText(/resumo derivado das mensagens/)).toBeInTheDocument()
    expect(within(panel).getByText(/guardado cifrado, por padrão por 90 dias/)).toBeInTheDocument()
    expect(within(panel).getByText(/mantém os itens resumidos/)).toBeInTheDocument()
  })

  it('erases the whole memory only after typing the confirmation word', async () => {
    const user = userEvent.setup()
    serve({ permissions: MANAGE_ALL, proposals: [] })
    vi.mocked(axios.delete).mockResolvedValue({ data: { items_deleted: 4, imports_discarded: 1 } })
    const panel = await openCopilot(user, 'Ajustes')
    await user.click(await within(panel).findByRole('button', { name: 'Apagar toda a memória do grupo' }))
    const dialog = (await screen.findByText('Apagar toda a memória do grupo?', { selector: 'h2' })).closest('[role="dialog"]') as HTMLElement
    const confirm = within(dialog).getByRole('button', { name: 'Apagar toda a memória' })
    expect(confirm).toBeDisabled()
    await user.type(within(dialog).getByLabelText('Digite APAGAR para confirmar'), 'apagar')
    expect(confirm).toBeDisabled() // case matters
    await user.clear(within(dialog).getByLabelText('Digite APAGAR para confirmar'))
    await user.type(within(dialog).getByLabelText('Digite APAGAR para confirmar'), 'APAGAR')
    expect(confirm).toBeEnabled()
    await user.click(confirm)
    await waitFor(() => expect(axios.delete).toHaveBeenCalledTimes(1))
    expect(vi.mocked(axios.delete).mock.calls[0][0]).toMatch(/\/groups\/g-1\/ai\/memory$/)
  })
})
