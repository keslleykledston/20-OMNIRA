import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors contracts/openapi/group-ai-v1.yaml (ADR-0060): the group copilot. The AI opt-in of a group, the memory it keeps
// (typed items with verifiable evidence), the import of a conversation export and the proposals a person approves.
// The tenant is the session's (getTenantId), never a value this module invents; the server re-checks it against the memberships.
// Every route is behind a flag: with it off the routes answer 404 and the caller simply does not show the copilot.

export type MemoryKind = 'request' | 'handled_topic' | 'fact' | 'agreement' | 'participant_role'
export type MemoryTier = 'episodic' | 'consolidated'
export type MemoryStatus = 'open' | 'resolved' | 'superseded'
export type TruthLevel = 'said_in_group' | 'ai_inferred' | 'human_confirmed'
export type MemorySource = 'live' | 'import' | 'human' | 'consolidation'

export interface Evidence {
  /** A short excerpt, verified against the message. Absent once it was removed (history deleted or item consolidated). */
  quote?: string
  at: string
}

export interface MemoryItem {
  id: string
  kind: MemoryKind
  tier: MemoryTier
  status: MemoryStatus
  truth_level: TruthLevel
  summary: string
  actor_label: string
  evidence: Evidence[]
  pinned: boolean
  relevance: number
  last_reinforced_at: string
  source: MemorySource
  version: number
  created_at: string
  updated_at: string
}

export interface MemoryState {
  last_processed_at?: string | null
  last_run_at?: string | null
  last_run_ok?: boolean | null
  last_consolidated_at?: string | null
  analyze_requested_at?: string | null
  consolidate_requested_at?: string | null
  prompt_version?: string
  runs?: number
  input_tokens?: number
  output_tokens?: number
}

export interface MemoryList {
  group: { id?: string; name?: string; enabled?: boolean; ai_enabled?: boolean }
  items: MemoryItem[]
  count: number
  state: MemoryState
}

export interface MemorySearch {
  items: MemoryItem[]
  count: number
}

export interface MemoryListParams {
  status?: MemoryStatus
  kind?: MemoryKind
  tier?: MemoryTier
  q?: string
  include_superseded?: boolean
}

export interface CreateMemoryItemRequest {
  kind: MemoryKind
  summary: string
  actor_label?: string
  status?: 'open' | 'resolved'
}

export interface PatchMemoryItemRequest {
  summary?: string
  status?: 'open' | 'resolved'
  pinned?: boolean
  confirm?: boolean
  /** The version the screen read; a different one answers 409. */
  version?: number
}

export type ImportStatus = 'uploaded' | 'extracting' | 'extracted' | 'failed' | 'discarded'
export type DiscardReason = 'confirmed' | 'expired' | 'history_deleted' | 'group_ai_disabled' | 'memory_erased'

export interface GroupImport {
  id: string
  status: ImportStatus
  format: 'whatsapp_txt' | 'whatsapp_zip'
  source_filename?: string
  sha256: string
  size_bytes: number
  messages_total: number
  messages_parsed: number
  messages_rejected?: number
  messages_duplicate?: number
  rejected_reasons?: Record<string, number>
  range_from?: string | null
  range_to?: string | null
  batches_total?: number
  batches_ok?: number
  batches_failed?: number
  items_created?: number
  /** Everything was READ. It does NOT mean everything was captured. */
  coverage_complete: boolean
  prompt_version?: string
  extract_runs?: number
  created_at: string
  extracted_at?: string | null
  expires_at: string
  discarded_at?: string | null
  discard_reason?: DiscardReason | null
  raw_available: boolean
  version?: number
}

export interface ImportList {
  items: GroupImport[]
  count: number
}

export type ProposalKind = 'case' | 'task'
export type ProposalStatus = 'pending_approval' | 'rejected' | 'created' | 'failed'
export type ProposalPriority = 'low' | 'normal' | 'high' | 'urgent'

export interface Proposal {
  id: string
  memory_item_id?: string | null
  kind: ProposalKind
  title: string
  description: string
  priority: ProposalPriority
  due_at?: string | null
  proposed_by: 'ai' | 'human'
  status: ProposalStatus
  decided_at?: string | null
  reject_reason?: string
  created_case_id?: string | null
  created_task_id?: string | null
  version: number
  created_at: string
}

export interface ProposalList {
  items: Proposal[]
  count: number
}

export interface EditProposalRequest {
  title?: string
  description?: string
  priority?: ProposalPriority
  due_at?: string | null
  version?: number
}

export interface SetGroupAIResponse {
  id: string
  ai_enabled: boolean
  imports_discarded: number
}

export interface EraseResponse {
  items_deleted: number
  imports_discarded: number
}

/** A failed call, reduced to the HTTP status (0 = no answer). The body is never kept: the server's errors are plain text and the screen says it in its own words. */
export class GroupAiError extends Error {
  status: number
  constructor(status: number) {
    super(`group ai request failed (${status})`)
    this.status = status
  }
}

/** Feature detection: the routes are not registered while the backend flag is off, so 404 means "there is no copilot", not "something broke". */
export function isCopilotAbsent(err: unknown): boolean {
  return err instanceof GroupAiError && err.status === 404
}

/** The copilot must not show at all: it is off for this tenant (404) or this person may not read it (403). Neither is an error to display. */
export function isCopilotHidden(err: unknown): boolean {
  return err instanceof GroupAiError && (err.status === 404 || err.status === 403)
}

const base = (groupId: string) => `${API_BASE}/tenants/${getTenantId()}/groups/${groupId}/ai`
const cfg = () => ({ headers: authHeaders() })

async function callFull<T>(fn: () => Promise<{ data: T; status?: number }>): Promise<{ data: T; status: number }> {
  try {
    const r = await fn()
    return { data: r.data, status: r.status ?? 200 }
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw new GroupAiError((err as { response?: { status?: number } })?.response?.status ?? 0)
  }
}
const call = async <T>(fn: () => Promise<{ data: T; status?: number }>): Promise<T> => (await callFull(fn)).data

export const GROUP_IMPORT_MAX_BYTES = 50 * 1024 * 1024

export const groupAiAPI = {
  setAI: (groupId: string, enabled: boolean, discardImports = false) =>
    call<SetGroupAIResponse>(() => axios.patch(base(groupId), { enabled, ...(!enabled && discardImports ? { discard_imports: true } : {}) }, cfg())),

  memory: (groupId: string, params: MemoryListParams = {}) =>
    call<MemoryList>(() => axios.get(`${base(groupId)}/memory`, { ...cfg(), params })),
  searchMemory: (groupId: string, q: string) =>
    call<MemorySearch>(() => axios.get(`${base(groupId)}/memory/search`, { ...cfg(), params: { q } })),
  createItem: (groupId: string, body: CreateMemoryItemRequest) =>
    call<MemoryItem>(() => axios.post(`${base(groupId)}/memory`, body, cfg())),
  patchItem: (groupId: string, itemId: string, body: PatchMemoryItemRequest) =>
    call<MemoryItem>(() => axios.patch(`${base(groupId)}/memory/${itemId}`, body, cfg())),
  deleteItem: (groupId: string, itemId: string) => call<void>(() => axios.delete(`${base(groupId)}/memory/${itemId}`, cfg())),
  eraseMemory: (groupId: string) => call<EraseResponse>(() => axios.delete(`${base(groupId)}/memory`, cfg())),

  analyze: (groupId: string) => call<{ requested: boolean }>(() => axios.post(`${base(groupId)}/analyze`, {}, cfg())),
  consolidate: (groupId: string) => call<{ requested: boolean }>(() => axios.post(`${base(groupId)}/consolidate`, {}, cfg())),

  imports: (groupId: string) => call<ImportList>(() => axios.get(`${base(groupId)}/imports`, cfg())),
  /** The file is the request body, its name in ?filename=. Never put the file in a multipart form: the contract is a raw octet-stream. */
  uploadImport: (groupId: string, file: File) =>
    call<GroupImport>(() =>
      axios.post(`${base(groupId)}/imports`, file, {
        headers: { ...authHeaders(), 'Content-Type': 'application/octet-stream' },
        params: { filename: file.name.slice(0, 200) },
        maxBodyLength: Infinity,
      }),
    ),
  discardImport: (groupId: string, importId: string) => call<GroupImport>(() => axios.post(`${base(groupId)}/imports/${importId}/discard`, {}, cfg())),
  reprocessImport: (groupId: string, importId: string) => call<GroupImport>(() => axios.post(`${base(groupId)}/imports/${importId}/reprocess`, {}, cfg())),

  proposals: (groupId: string, status?: ProposalStatus) =>
    call<ProposalList>(() => axios.get(`${base(groupId)}/proposals`, { ...cfg(), params: status ? { status } : {} })),
  /** 201 = drafted now; 200 = the item already had a live proposal of that kind (returned as is). */
  draftProposal: async (groupId: string, memoryItemId: string, kind: ProposalKind) => {
    const r = await callFull<Proposal>(() => axios.post(`${base(groupId)}/proposals`, { memory_item_id: memoryItemId, kind }, cfg()))
    return { proposal: r.data, existing: r.status === 200 }
  },
  editProposal: (groupId: string, proposalId: string, body: EditProposalRequest) =>
    call<Proposal>(() => axios.patch(`${base(groupId)}/proposals/${proposalId}`, body, cfg())),
  approveProposal: (groupId: string, proposalId: string) =>
    call<Proposal>(() => axios.post(`${base(groupId)}/proposals/${proposalId}/approve`, {}, cfg())),
  rejectProposal: (groupId: string, proposalId: string, reason?: string) =>
    call<Proposal>(() => axios.post(`${base(groupId)}/proposals/${proposalId}/reject`, reason ? { reason } : {}, cfg())),
}

export type GroupAiAction =
  | 'toggle'
  | 'search'
  | 'create_item'
  | 'patch_item'
  | 'delete_item'
  | 'erase'
  | 'analyze'
  | 'consolidate'
  | 'upload'
  | 'discard'
  | 'reprocess'
  | 'draft'
  | 'edit_proposal'
  | 'approve'
  | 'reject'
  | 'load'

/** What to tell the person. The same status means different things per action (a 409 is "stale screen" on an edit and "already imported" on an upload), so the action is part of the key. */
export function groupAiErrorMessage(err: unknown, action: GroupAiAction, kind?: ProposalKind): string {
  const status = err instanceof GroupAiError ? err.status : 0
  if (status === 401) return 'Sua sessão expirou. Entre de novo.'
  if (status === 403) {
    if (action === 'approve') {
      return `Você não tem permissão para criar ${kind === 'task' ? 'tarefas' : 'ocorrências'} (service.create). A proposta continua aguardando aprovação.`
    }
    return 'Você não tem permissão para esta ação no copiloto do grupo.'
  }
  if (status === 404) return 'Isto não existe mais, ou o copiloto não está disponível. Atualize a tela.'
  if (status === 0) return 'Sem resposta do servidor. Confira a conexão e tente de novo.'
  switch (action) {
    case 'toggle':
      return status === 409 ? 'O grupo precisa estar habilitado em "Gerenciar grupos" antes de ligar a IA.' : 'Não foi possível mudar a IA do grupo. Tente de novo.'
    case 'search':
      return status === 409 ? 'A busca precisa da IA ligada neste grupo.' : 'Não foi possível buscar. Tente de novo.'
    case 'create_item':
      if (status === 409) return 'Já existe um item igual na memória, ou a IA está desligada neste grupo.'
      if (status === 400) return 'O texto não foi aceito. Confira o tamanho e não inclua senhas, chaves ou tokens.'
      return 'Não foi possível registrar o item. Tente de novo.'
    case 'patch_item':
      if (status === 409) return 'Este item foi alterado por outra pessoa (ou condensado pela IA) enquanto você olhava. Recarregamos a lista: confira e faça a mudança de novo.'
      if (status === 400) return 'O texto não foi aceito. Confira o tamanho e não inclua senhas, chaves ou tokens.'
      return 'Não foi possível salvar o item. Tente de novo.'
    case 'delete_item':
      return 'Não foi possível apagar o item. Tente de novo.'
    case 'erase':
      return 'Não foi possível apagar a memória. Nada garante que tudo foi apagado: confira a lista e tente de novo.'
    case 'analyze':
    case 'consolidate':
      return status === 409
        ? 'Não foi possível pedir agora: a IA precisa estar ligada neste grupo.'
        : 'Não foi possível pedir a leitura agora. Tente mais tarde.'
    case 'upload':
      if (status === 409) return 'Este arquivo já foi importado neste grupo (ou a IA está desligada).'
      if (status === 413) return 'O arquivo passa de 50 MiB.'
      if (status === 422) return 'Não foi possível ler este arquivo. Envie a exportação da conversa do WhatsApp (.txt ou .zip).'
      if (status === 503) return 'Não foi possível verificar o arquivo com o antivírus agora, então ele foi recusado. Tente de novo mais tarde.'
      return 'Não foi possível importar o arquivo. Tente de novo.'
    case 'discard':
      return status === 409
        ? 'O arquivo original só pode ser descartado depois que todas as mensagens foram lidas. Ele será descartado sozinho no fim do prazo.'
        : 'Não foi possível descartar o arquivo. Tente de novo.'
    case 'reprocess':
      return status === 409
        ? 'Não foi possível reprocessar: o arquivo original já foi descartado, a leitura ainda está em andamento, ou ele já foi lido por completo com a versão atual.'
        : 'Não foi possível reprocessar o arquivo. Tente de novo.'
    case 'draft':
      return status === 409 ? 'Não foi possível criar a proposta para este item agora.' : 'Não foi possível criar a proposta. Tente de novo.'
    case 'edit_proposal':
      if (status === 409) return 'Esta proposta mudou ou já foi decidida enquanto você olhava. Recarregamos a lista.'
      return 'Não foi possível salvar a proposta. Confira os campos e tente de novo.'
    case 'approve':
      return status === 409 ? 'Esta proposta já foi rejeitada ou mudou. Recarregamos a lista.' : 'Não foi possível aprovar. A proposta continua aguardando.'
    case 'reject':
      return status === 409 ? 'Esta proposta já foi decidida. Recarregamos a lista.' : 'Não foi possível rejeitar. Tente de novo.'
    default:
      return 'Não foi possível carregar. Tente de novo.'
  }
}
