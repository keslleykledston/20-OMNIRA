import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Espelha contracts/openapi/attendance-v1.yaml (ADR-0020). A autoridade é sempre o backend: a UI só evita oferecer ações que ele
// recusaria. Finalizar encerra o EPISÓDIO de atendimento (a próxima mensagem do contato abre um atendimento novo).

export type CloseReason = 'resolved' | 'no_response' | 'duplicate' | 'spam' | 'transferred' | 'other'
export type FollowUpKind = 'pending' | 'promise' | 'info'
export type FollowUpStatus = 'open' | 'done' | 'dropped'
export type Truth = 'agent_confirmed' | 'ai_inferred'

export const CLOSE_REASON_LABEL: Record<CloseReason, string> = {
  resolved: 'Resolvido',
  no_response: 'Cliente sem resposta',
  duplicate: 'Atendimento duplicado',
  spam: 'Spam / engano',
  transferred: 'Tratado em outro canal ou setor',
  other: 'Outro motivo',
}

export const FOLLOW_UP_KIND_LABEL: Record<FollowUpKind, string> = {
  pending: 'Pendência',
  promise: 'Promessa ao cliente',
  info: 'Para lembrar',
}

export interface FollowUp {
  id: string
  conversation_id: string
  kind: FollowUpKind
  text: string
  owner_user_id: string | null
  due_at: string | null
  status: FollowUpStatus
  truth: Truth
  created_at: string
  resolved_at: string | null
  resolution_note: string
}

export interface Closure {
  id: string
  conversation_id: string
  closed_by_user_id: string | null
  source: 'agent' | 'supervisor' | 'system'
  reason: CloseReason
  note: string
  summary: string
  summary_truth: Truth
  local_tickets_closed: number
  tickets_kept: number
  created_at: string
  follow_ups: FollowUp[]
}

export interface AttendanceHistory {
  contact_id: string
  attendances: Closure[]
  open_follow_ups: FollowUp[]
}

export interface HistoryHit {
  at: string
  role: 'customer' | 'agent'
  snippet: string
  conversation_id: string
}

export interface ClosingSuggestion {
  summary: string
  summary_truth: 'ai_inferred'
  follow_ups: { kind: FollowUpKind; text: string }[]
  model: string
  based_on_messages: number
}

export interface FollowUpInput {
  kind: FollowUpKind
  text: string
  owner_user_id?: string | null
  due_at?: string | null
}

export interface FinalizeBody {
  reason: CloseReason
  note?: string
  summary?: string
  summary_truth?: Truth
  follow_ups?: FollowUpInput[]
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`
const h = () => ({ headers: authHeaders() })

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const attendanceAPI = {
  finalize: (conversationId: string, body: FinalizeBody) =>
    call<{ changed: boolean; closure?: Closure }>(() => axios.post(`${base()}/inbox/conversations/${conversationId}/finalize`, body, h())),
  suggestClosing: (conversationId: string) =>
    call<ClosingSuggestion>(() => axios.post(`${base()}/inbox/conversations/${conversationId}/finalize/suggest`, {}, h())),
  contextOf: (conversationId: string, limit = 5) =>
    call<AttendanceHistory>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/attendance-context`, { ...h(), params: { limit } })),
  searchHistory: (conversationId: string, q: string, limit = 5) =>
    call<{ items: HistoryHit[] }>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/history-search`, { ...h(), params: { q, limit } })).then((r) => r.items),
  resolveFollowUp: (id: string, body: { status: 'done' | 'dropped'; note?: string }) =>
    call<FollowUp>(() => axios.post(`${base()}/follow-ups/${id}/resolve`, body, h())),
}

// Mensagem para o operador a partir da resposta de erro do backend (nunca expõe detalhes internos).
export function attendanceErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number; data?: { detail?: string; error?: string } } })?.response?.status
  const data = (err as { response?: { data?: { detail?: string; error?: string } } })?.response?.data
  if (status === 403) return 'Só o responsável pela conversa (ou um supervisor) pode finalizar o atendimento.'
  if (status === 404) return 'Conversa não encontrada.'
  if (status === 409 && data?.error === 'already_resolved') return 'Esta pendência já foi resolvida.'
  if (status === 422 && data?.error === 'nothing_to_suggest') return 'Não há mensagens de texto para resumir.'
  if (status === 503 && data?.error === 'ai_disabled') return 'A sugestão por IA não está ativada neste ambiente. Você pode finalizar normalmente.'
  if (status === 503) return 'A sugestão por IA não está disponível agora. Você pode finalizar normalmente.'
  if (status === 422) return 'Esta conversa não pode ser finalizada (não é um atendimento a um contato).'
  if (status === 400 && data?.detail?.includes('query')) return 'Digite de 2 a 100 caracteres, sem senhas ou chaves.'
  if (status === 400 && data?.detail) return data.detail.replace(/^attendance: invalid input: /, '')
  return 'Não foi possível concluir. Tente novamente.'
}
