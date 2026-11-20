import { beforeEach, describe, expect, it, vi } from 'vitest'
import axios from 'axios'
import {
  GroupAiError,
  groupAiAPI,
  groupAiErrorMessage,
  isCopilotAbsent,
  isCopilotHidden,
  type GroupImport,
  type MemoryItem,
  type Proposal,
} from '../lib/groupAi'
import {
  analysisPending,
  canDiscardImport,
  consolidationPending,
  coverageVerdict,
  discardHint,
  filterMemory,
  formatBytes,
  fromLocalInput,
  lastRunText,
  openRequestCount,
  proposalChanged,
  proposalOutcome,
  proposalPatch,
  rejectedReasonsText,
  toLocalInput,
  validateImportFile,
} from '../lib/groupAiModel'
import { PERMISSION_GROUPS } from '../lib/permissions'
import { setSession, TENANT } from './testUtils'

vi.mock('axios')

const G = 'g-1'
const item = (over: Partial<MemoryItem> = {}): MemoryItem => ({
  id: 'i-1',
  kind: 'request',
  tier: 'episodic',
  status: 'open',
  truth_level: 'ai_inferred',
  summary: 'Cliente pediu a segunda via do boleto',
  actor_label: 'Rafael',
  evidence: [],
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
  sha256: 'x',
  size_bytes: 2048,
  messages_total: 100,
  messages_parsed: 90,
  coverage_complete: true,
  created_at: '2026-11-01T10:00:00Z',
  expires_at: '2026-01-30T10:00:00Z',
  raw_available: true,
  ...over,
})
const proposal = (over: Partial<Proposal> = {}): Proposal => ({
  id: 'p-1',
  kind: 'case',
  title: 'Segunda via do boleto',
  description: 'Enviar o boleto',
  priority: 'normal',
  proposed_by: 'ai',
  status: 'pending_approval',
  version: 2,
  created_at: '2026-11-01T10:00:00Z',
  ...over,
})

beforeEach(() => {
  vi.resetAllMocks()
  localStorage.clear()
  setSession()
})

describe('groupAiAPI', () => {
  it('reads the memory of the group under the session tenant, with the bearer token, and never sends a tenant of its own', async () => {
    vi.mocked(axios.get).mockResolvedValue({ data: { group: {}, items: [], count: 0, state: {} } })
    await groupAiAPI.memory(G, { include_superseded: true })
    const [url, cfg] = vi.mocked(axios.get).mock.calls[0] as [string, any]
    expect(url).toBe(`/api/v1/tenants/${TENANT}/groups/${G}/ai/memory`)
    expect(cfg.headers.Authorization).toBe('Bearer tok')
    expect(cfg.params).toEqual({ include_superseded: true })
    expect(JSON.stringify(cfg)).not.toContain('tenant_id')
  })

  it('edits an item with only the fields given plus the version the screen read', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: item() })
    await groupAiAPI.patchItem(G, 'i-1', { summary: 'novo texto', version: 3 })
    const [url, body] = vi.mocked(axios.patch).mock.calls[0] as [string, any]
    expect(url).toMatch(/\/ai\/memory\/i-1$/)
    expect(body).toEqual({ summary: 'novo texto', version: 3 })
  })

  it('sends discard_imports only when turning the AI off', async () => {
    vi.mocked(axios.patch).mockResolvedValue({ data: { id: G, ai_enabled: true, imports_discarded: 0 } })
    await groupAiAPI.setAI(G, true, true)
    await groupAiAPI.setAI(G, false, true)
    await groupAiAPI.setAI(G, false)
    const bodies = vi.mocked(axios.patch).mock.calls.map((c) => c[1])
    expect(bodies).toEqual([{ enabled: true }, { enabled: false, discard_imports: true }, { enabled: false }])
  })

  it('uploads the file as the raw request body with its name in ?filename=, not as a form', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: imp(), status: 202 })
    const file = new File(['[01/11/2026 10:00] Rafael: oi'], 'Conversa.txt', { type: 'text/plain' })
    await groupAiAPI.uploadImport(G, file)
    const [url, body, cfg] = vi.mocked(axios.post).mock.calls[0] as [string, any, any]
    expect(url).toMatch(/\/ai\/imports$/)
    expect(body).toBe(file)
    expect(cfg.headers['Content-Type']).toBe('application/octet-stream')
    expect(cfg.params).toEqual({ filename: 'Conversa.txt' })
  })

  it('tells a new proposal (201) from an existing live one (200)', async () => {
    vi.mocked(axios.post).mockResolvedValueOnce({ data: proposal(), status: 201 }).mockResolvedValueOnce({ data: proposal(), status: 200 })
    expect((await groupAiAPI.draftProposal(G, 'i-1', 'case')).existing).toBe(false)
    expect((await groupAiAPI.draftProposal(G, 'i-1', 'case')).existing).toBe(true)
    expect(vi.mocked(axios.post).mock.calls[0][1]).toEqual({ memory_item_id: 'i-1', kind: 'case' })
  })

  it('rejects with an optional reason', async () => {
    vi.mocked(axios.post).mockResolvedValue({ data: proposal({ status: 'rejected' }) })
    await groupAiAPI.rejectProposal(G, 'p-1')
    await groupAiAPI.rejectProposal(G, 'p-1', 'duplicada')
    expect(vi.mocked(axios.post).mock.calls.map((c) => c[1])).toEqual([{}, { reason: 'duplicada' }])
  })

  it('reduces a failure to its HTTP status and keeps no body', async () => {
    vi.mocked(axios.get).mockRejectedValue({ response: { status: 404, data: 'texto do servidor com conteudo' } })
    const err = await groupAiAPI.memory(G).catch((e) => e)
    expect(err).toBeInstanceOf(GroupAiError)
    expect(err.status).toBe(404)
    expect(JSON.stringify(err)).not.toContain('conteudo')
    expect(isCopilotAbsent(err)).toBe(true)
    expect(isCopilotHidden(err)).toBe(true)
    expect(isCopilotAbsent(new GroupAiError(403))).toBe(false)
    expect(isCopilotHidden(new GroupAiError(403))).toBe(true)
    expect(isCopilotHidden(new GroupAiError(500))).toBe(false)
  })
})

describe('groupAiErrorMessage', () => {
  it('gives the same status a different meaning per action', () => {
    expect(groupAiErrorMessage(new GroupAiError(409), 'patch_item')).toMatch(/alterado por outra pessoa/)
    expect(groupAiErrorMessage(new GroupAiError(409), 'upload')).toMatch(/já foi importado/)
    expect(groupAiErrorMessage(new GroupAiError(409), 'discard')).toMatch(/depois que todas as mensagens foram lidas/)
    expect(groupAiErrorMessage(new GroupAiError(413), 'upload')).toMatch(/50 MiB/)
  })
  it('names the missing permission when approving a proposal is refused, per kind', () => {
    expect(groupAiErrorMessage(new GroupAiError(403), 'approve', 'case')).toMatch(/criar ocorrências \(service\.create\).*continua aguardando/)
    expect(groupAiErrorMessage(new GroupAiError(403), 'approve', 'task')).toMatch(/criar tarefas/)
  })
  it('never echoes anything the server said', () => {
    const e = new GroupAiError(400)
    ;(e as any).body = 'senha: 1234'
    expect(groupAiErrorMessage(e, 'create_item')).not.toContain('1234')
  })
})

describe('groupAiModel', () => {
  it('filters by kind and status; "active" leaves superseded out', () => {
    const items = [item({ id: 'a' }), item({ id: 'b', kind: 'fact', status: 'resolved' }), item({ id: 'c', status: 'superseded' })]
    expect(filterMemory(items, { kind: '', status: 'active' }).map((i) => i.id)).toEqual(['a', 'b'])
    expect(filterMemory(items, { kind: 'fact', status: 'active' }).map((i) => i.id)).toEqual(['b'])
    expect(filterMemory(items, { kind: '', status: 'superseded' }).map((i) => i.id)).toEqual(['c'])
    expect(openRequestCount(items)).toBe(1)
  })

  it('knows a "read now" is still waiting until a run happens after it was asked', () => {
    expect(analysisPending(undefined)).toBe(false)
    expect(analysisPending({ analyze_requested_at: '2026-11-02T10:00:00Z', last_run_at: '2026-11-01T10:00:00Z' })).toBe(true)
    expect(analysisPending({ analyze_requested_at: '2026-11-02T10:00:00Z', last_run_at: '2026-11-03T10:00:00Z' })).toBe(false)
    expect(analysisPending({ analyze_requested_at: '2026-11-02T10:00:00Z' })).toBe(true)
    expect(consolidationPending({ consolidate_requested_at: '2026-11-02T10:00:00Z', last_consolidated_at: null })).toBe(true)
    expect(consolidationPending({ consolidate_requested_at: '2026-11-02T10:00:00Z', last_consolidated_at: '2026-11-05T10:00:00Z' })).toBe(false)
  })

  it('is honest about a reading that never ran or failed', () => {
    expect(lastRunText({})).toBe('A IA ainda não leu este grupo.')
    expect(lastRunText({ last_run_at: '2026-11-01T10:00:00Z', last_run_ok: false })).toMatch(/falhou/)
    expect(lastRunText({ last_run_at: '2026-11-01T10:00:00Z', last_run_ok: true })).not.toMatch(/falhou/)
  })

  it('words coverage as "everything was read", never as "everything was captured"', () => {
    const complete = coverageVerdict(imp())
    expect(complete.title).toBe('Todas as mensagens foram lidas')
    expect(complete.detail).toMatch(/não que tudo foi registrado/)
    const all = `${complete.title} ${complete.detail}`
    expect(all).not.toMatch(/capturad/i)
    expect(all).not.toMatch(/100\s?%|cobertura total|garant/i)

    const partial = coverageVerdict(imp({ coverage_complete: false, batches_failed: 2 }))
    expect(partial.tone).toBe('warning')
    expect(partial.title).toBe('Cobertura incompleta')
    expect(partial.detail).toMatch(/2 lotes falharam/)
    expect(coverageVerdict(imp({ status: 'extracting', coverage_complete: false })).title).toBe('Leitura em andamento')
    expect(coverageVerdict(imp({ status: 'failed', coverage_complete: false })).tone).toBe('danger')
  })

  it('allows discarding the original only while it exists and everything was read', () => {
    expect(canDiscardImport(imp())).toBe(true)
    expect(canDiscardImport(imp({ coverage_complete: false }))).toBe(false)
    expect(canDiscardImport(imp({ raw_available: false }))).toBe(false)
    expect(discardHint(imp())).toBe('')
    expect(discardHint(imp({ coverage_complete: false }))).toMatch(/descartado sozinho em/)
    expect(discardHint(imp({ coverage_complete: false, raw_available: false }))).toBe('')
  })

  it('checks the file before sending it', () => {
    expect(validateImportFile({ name: 'a.txt', size: 10 })).toBeNull()
    expect(validateImportFile({ name: 'A.ZIP', size: 10 })).toBeNull()
    expect(validateImportFile({ name: 'a.pdf', size: 10 })).toMatch(/\.txt ou \.zip/)
    expect(validateImportFile({ name: 'a.txt', size: 0 })).toMatch(/vazio/)
    expect(validateImportFile({ name: 'a.txt', size: 50 * 1024 * 1024 + 1 })).toMatch(/50 MiB/)
  })

  it('formats sizes and the rejected reasons', () => {
    expect(formatBytes(512)).toBe('512 B')
    expect(formatBytes(2048)).toBe('2,0 KiB')
    expect(rejectedReasonsText({ system: 4, bad_date: 1, empty: 0 })).toBe('4 linhas do sistema, 1 data não reconhecida')
  })

  it('builds a proposal edit with only what changed and the version, and round-trips the due date', () => {
    const p = proposal()
    const same = proposalPatch(p, { title: p.title, description: p.description, priority: p.priority, due: '' })
    expect(same).toEqual({ version: 2 })
    expect(proposalChanged(same)).toBe(false)
    const changed = proposalPatch(p, { title: ' Novo ', description: p.description, priority: 'urgent', due: '2026-12-01T09:30' })
    expect(changed).toMatchObject({ version: 2, title: 'Novo', priority: 'urgent' })
    expect(changed.due_at).toBe(new Date('2026-12-01T09:30').toISOString())
    expect(proposalChanged(changed)).toBe(true)
    // clearing the date sends null, not nothing
    expect(proposalPatch(proposal({ due_at: '2026-12-01T12:30:00Z' }), { title: p.title, description: p.description, priority: p.priority, due: '' }).due_at).toBeNull()
    expect(toLocalInput(fromLocalInput('2026-12-01T09:30'))).toBe('2026-12-01T09:30')
    expect(fromLocalInput('')).toBeNull()
  })

  it('confirms what a created proposal made, only from the ids the server returned', () => {
    expect(proposalOutcome(proposal())).toBe('')
    expect(proposalOutcome(proposal({ status: 'created', created_case_id: 'abcdef12-0000' }))).toBe('Ocorrência criada (registro abcdef12).')
    expect(proposalOutcome(proposal({ kind: 'task', status: 'created', created_task_id: '12345678-0000' }))).toBe('Tarefa criada (registro 12345678).')
  })
})

describe('permission catalogue', () => {
  it('lists the four group copilot keys under Grupos', () => {
    const keys = PERMISSION_GROUPS.find((g) => g.title === 'Grupos')!.items.map((i) => i.key)
    expect(keys).toEqual(expect.arrayContaining(['group.ai.read', 'group.ai.manage', 'group.import', 'group.memory.erase']))
  })
})
