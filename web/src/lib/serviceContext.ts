import axios from 'axios'
import { API_BASE } from './config'
import { ACTING_HEADER } from './acting'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'
import type { DelegatedTarget } from './attendance'

// Espelha contracts/openapi/service-context-v1.yaml (ADR-0049). A autoridade é sempre o backend: a UI só evita oferecer ações que ele recusaria.
// Atendimento (sessão) = camada sobre a conversa, com protocolo AT-n por empresa. Encerrar o atendimento NÃO é finalizar a conversa.

export type SessionStatus = 'open' | 'in_progress' | 'waiting' | 'closed' | 'abandoned'
export type CanonicalResult =
  | 'information_provided'
  | 'resolved_first_contact'
  | 'task_created'
  | 'case_created'
  | 'external_service_order'
  | 'transferred'
  | 'abandoned'
  | 'other'
export type TaskStatus = 'open' | 'in_progress' | 'done' | 'cancelled'
export type Priority = 'low' | 'normal' | 'high' | 'urgent'

export const SESSION_STATUS_LABEL: Record<SessionStatus, string> = {
  open: 'Aberto',
  in_progress: 'Em andamento',
  waiting: 'Aguardando',
  closed: 'Encerrado',
  abandoned: 'Abandonado',
}

export const CANONICAL_RESULT_LABEL: Record<CanonicalResult, string> = {
  information_provided: 'Informação prestada',
  resolved_first_contact: 'Resolvido no primeiro contato',
  task_created: 'Tarefa criada',
  case_created: 'Ocorrência criada',
  external_service_order: 'Ordem de serviço no ERP',
  transferred: 'Transferido',
  abandoned: 'Abandonado',
  other: 'Outro',
}

export const TASK_STATUS_LABEL: Record<TaskStatus, string> = { open: 'Aberta', in_progress: 'Em andamento', done: 'Concluída', cancelled: 'Cancelada' }

export interface ServiceSession {
  id: string
  protocol: string
  external_protocol: string | null
  conversation_id: string | null
  contact_id: string | null
  customer_id: string | null
  queue_id: string | null
  category_id: string | null
  status: SessionStatus
  resolution_code: string | null
  resolution_canonical?: CanonicalResult
  resolution_note: string
  origin_channel: string
  opened_at: string
  closed_at: string | null
  reopen_count: number
  version: number
}

export interface ServiceTask {
  id: string
  title: string
  description: string
  status: TaskStatus
  priority: Priority
  assigned_user_id: string | null
  due_at: string | null
  overdue: boolean
  session_id: string | null
  case_id: string | null
  version: number
}

export interface ResultCode { id: string; code: string; canonical_code: CanonicalResult; label: string; active: boolean }
export type CaseStatus = 'open' | 'investigating' | 'waiting' | 'resolved' | 'closed' | 'cancelled'
export const CASE_STATUS_LABEL: Record<CaseStatus, string> = {
  open: 'Aberta', investigating: 'Em investigação', waiting: 'Aguardando', resolved: 'Resolvida', closed: 'Encerrada', cancelled: 'Cancelada',
}
export interface ServiceCase {
  id: string
  case_number: string
  title: string
  status: CaseStatus
  priority: Priority
  origin_session_id: string | null
  opened_at: string
  version: number
}

export interface TimelineItem {
  id: string
  event_type: string
  occurred_at: string
  actor_type: 'user' | 'system' | 'flow' | 'ai' | 'integration'
  note?: string
  derived?: boolean
}

export type RequestState = 'in_flight' | 'confirmed_success' | 'confirmed_failure' | 'outcome_unknown'
export const REQUEST_STATE_LABEL: Record<RequestState, string> = {
  in_flight: 'Enviando ao ERP', confirmed_success: 'Criada no ERP', confirmed_failure: 'Recusada pelo ERP', outcome_unknown: 'Sem resposta do ERP: confirmar antes de repetir',
}
export interface ExternalRequest {
  id: string
  title: string
  state: RequestState
  operation: string
  created_at: string
}

// What the history shows for each stored event (ids and enums only; never message text). An unknown type is shown by its raw name: the module may grow.
export const EVENT_LABEL: Record<string, string> = {
  'service_session.opened': 'Protocolo aberto',
  'service_session.classified': 'Atendimento classificado',
  'service_session.status_changed': 'Estado do atendimento mudou',
  'service_session.closed': 'Protocolo encerrado',
  'service_session.reopened': 'Protocolo reaberto',
  'service_task.created': 'Tarefa criada',
  'service_task.assigned': 'Tarefa atribuída',
  'service_task.status_changed': 'Tarefa mudou de estado',
  'service_task.updated': 'Tarefa alterada',
  'service_case.opened': 'Ocorrência aberta',
  'service_case.assigned': 'Ocorrência atribuída',
  'service_case.status_changed': 'Ocorrência mudou de estado',
  'service_case.updated': 'Ocorrência alterada',
  'service_case.session_linked': 'Ocorrência ligada ao protocolo',
  'external_record.linked': 'Registro do ERP ligado',
  'external_record.unlinked': 'Registro do ERP desligado',
  'external_record.status_changed': 'Registro do ERP mudou de estado',
  'external_request.created': 'Pedido enviado ao ERP',
  'external_request.state_changed': 'Pedido ao ERP mudou de estado',
  'conversation.assignment_changed': 'Conversa passou para outra pessoa',
}
export const ACTOR_LABEL: Record<TimelineItem['actor_type'], string> = { user: 'Pessoa', system: 'Sistema', flow: 'Fluxo', ai: 'IA', integration: 'Integração' }

export interface ProtocolPrefix { prefix: string; configured: boolean; locked: boolean; next_protocol: string }
export interface Category { id: string; code: string; label: string; active: boolean }

// O servidor fecha o atendimento com um dos oito códigos internos ou um código ativo da empresa. A lista da empresa vem do catálogo; os internos vêm daqui.
export const BUILTIN_RESULTS: { code: CanonicalResult; label: string }[] = (
  ['information_provided', 'resolved_first_contact', 'task_created', 'case_created', 'external_service_order', 'transferred', 'other'] as CanonicalResult[]
).map((code) => ({ code, label: CANONICAL_RESULT_LABEL[code] }))

const base = (target?: DelegatedTarget) => `${API_BASE}/tenants/${target?.tenantId ?? getTenantId()}`
const h = (target?: DelegatedTarget, extra: Record<string, string> = {}) => ({
  headers: { ...authHeaders(), ...(target ? { [ACTING_HEADER]: `hub:${target.hubId}` } : {}), ...extra },
})
// Mutations of an existing thing are conditional on the version the screen saw (a stale screen gets 412, never silently overwrites).
const ifMatch = (version: number) => `"${version}"`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const serviceAPI = {
  // The active session of the conversation, or the latest one. 404 = none yet (or the module is off): the panel then offers to open one or hides itself.
  ofConversation: (conversationId: string, target?: DelegatedTarget) =>
    call<ServiceSession>(() => axios.get(`${base(target)}/conversations/${conversationId}/service-session`, h(target))),
  open: (conversationId: string, key: string, target?: DelegatedTarget) =>
    call<ServiceSession>(() => axios.post(`${base(target)}/service-sessions`, { conversation_id: conversationId }, h(target, { 'Idempotency-Key': key }))),
  setStatus: (s: ServiceSession, status: 'in_progress' | 'waiting', target?: DelegatedTarget) =>
    call<ServiceSession>(() => axios.post(`${base(target)}/service-sessions/${s.id}/status`, { status }, h(target, { 'If-Match': ifMatch(s.version) }))),
  close: (s: ServiceSession, body: { resolution_code?: string; note?: string; abandon?: boolean }, target?: DelegatedTarget) =>
    call<ServiceSession>(() => axios.post(`${base(target)}/service-sessions/${s.id}/close`, body, h(target, { 'If-Match': ifMatch(s.version) }))),
  tasksOfSession: (sessionId: string, target?: DelegatedTarget) =>
    call<{ items: ServiceTask[] }>(() => axios.get(`${base(target)}/service-tasks`, { ...h(target), params: { session_id: sessionId, limit: 50 } })).then((r) => r.items),
  createTask: (body: { title: string; session_id: string; priority?: Priority }, key: string, target?: DelegatedTarget) =>
    call<ServiceTask>(() => axios.post(`${base(target)}/service-tasks`, body, h(target, { 'Idempotency-Key': key }))),
  completeTask: (t: ServiceTask, target?: DelegatedTarget) =>
    call<ServiceTask>(() => axios.post(`${base(target)}/service-tasks/${t.id}/complete`, {}, h(target, { 'If-Match': ifMatch(t.version) }))),
  casesOfSession: (sessionId: string, target?: DelegatedTarget) =>
    call<{ items: ServiceCase[] }>(() => axios.get(`${base(target)}/service-cases`, { ...h(target), params: { session_id: sessionId, limit: 50 } })).then((r) => r.items),
  openCase: (body: { title: string; session_id: string }, key: string, target?: DelegatedTarget) =>
    call<ServiceCase>(() => axios.post(`${base(target)}/service-cases`, body, h(target, { 'Idempotency-Key': key }))),
  timeline: (sessionId: string, target?: DelegatedTarget) =>
    call<{ items: TimelineItem[] }>(() => axios.get(`${base(target)}/service-sessions/${sessionId}/timeline`, { ...h(target), params: { order: 'desc', limit: 50 } })).then((r) => r.items),
  // The requests made to the ERP are the company's own (never delegable): a member asks, a Hub agent does not.
  externalRequestsOfSession: (sessionId: string) =>
    call<{ items: ExternalRequest[] }>(() => axios.get(`${base()}/external-requests`, { ...h(), params: { session_id: sessionId, limit: 20 } })).then((r) => r.items),
  // Members only (never delegable): the company's prefix and the next protocol number.
  protocolPrefix: () => call<ProtocolPrefix>(() => axios.get(`${base()}/service-settings/protocol-prefix`, h())),
  setProtocolPrefix: (prefix: string) =>
    call<ProtocolPrefix>(() => axios.put(`${base()}/service-settings/protocol-prefix`, { prefix }, h())),
  resultCodes: (target?: DelegatedTarget) =>
    call<{ items: ResultCode[] }>(() => axios.get(`${base(target)}/service-result-codes`, h(target))).then((r) => r.items.filter((c) => c.active)),
}

export function newKey(): string {
  return globalThis.crypto?.randomUUID?.() ?? `k-${Date.now()}-${Math.random().toString(36).slice(2, 12)}`
}

export function isModuleAbsent(err: unknown): boolean {
  const s = (err as { response?: { status?: number } })?.response?.status
  return s === 404 || s === 501
}

// Mensagem para o operador (nunca expõe detalhes internos).
export function serviceErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Você não tem permissão para esta ação.'
  if (status === 404) return 'Atendimento não encontrado.'
  if (status === 409) return 'O atendimento mudou de estado. Atualize e tente de novo.'
  if (status === 412 || status === 428) return 'Outra pessoa alterou este atendimento. Recarregamos os dados; confira e tente de novo.'
  if (status === 422) return 'Não foi possível: confira os dados informados.'
  return 'Não foi possível concluir. Tente novamente.'
}

export const PREFIX_PATTERN = /^[A-Za-z]{2,6}$/

export function prefixErrorMessage(err: unknown): string {
  const r = (err as { response?: { status?: number; data?: { error?: string } } })?.response
  if (r?.status === 403) return 'Somente administradores podem escolher o prefixo.'
  if (r?.status === 409 && r.data?.error === 'prefix_locked') return 'O prefixo não muda depois que o primeiro protocolo é emitido.'
  if (r?.status === 409) return 'Outra empresa já usa este prefixo. Escolha outro.'
  if (r?.status === 400) return 'Use de 2 a 6 letras, sem números nem espaços.'
  return 'Não foi possível salvar o prefixo. Tente novamente.'
}
