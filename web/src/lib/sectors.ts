import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Cliente da API de setores, horários, escalas, plantões, transbordo e SLA (ADR-0044). Espelha internal/sectors/adapters. A interface chama
// as agendas de "horários". Nenhuma regra de horário é decidida aqui: o servidor valida e executa.

export interface Sector {
  id: string
  name: string
  active: boolean
  calendar_id?: string
  queues: { id: string; name: string }[]
}

export type CalendarKind = '24x7' | 'business' | 'custom'
export type Weekday = 'mon' | 'tue' | 'wed' | 'thu' | 'fri' | 'sat' | 'sun'
export interface Interval { start: string; end: string }
export interface WeeklyDay { day: Weekday; intervals: Interval[] }
export interface CalendarException { name?: string; from: string; to: string; closed: boolean; intervals?: Interval[] }
export interface CalendarHoliday { name?: string; date: string; intervals?: Interval[] }
export interface CalendarSpec {
  kind: CalendarKind
  timezone: string
  weekly?: WeeklyDay[]
  exceptions?: CalendarException[]
  holidays?: CalendarHoliday[]
}
export interface Suspension { id: string; from: string; to: string; reason?: string }
export interface CalendarSummary { id: string; name: string; kind: CalendarKind; timezone: string; revision: number; in_use: number; updated_at: string }
export interface Calendar { id: string; name: string; revision: number; definition: CalendarSpec; suspensions: Suspension[]; updated_at: string }

export interface Availability {
  open: boolean
  at: string
  calendar_source: 'queue' | 'sector' | 'tenant' | 'none'
  calendar_id?: string
  timezone?: string
  next_open?: string
  next_close?: string
}

export interface AgentShift { user_id: string; calendar_id: string; queue_id?: string; sector_id?: string }
export interface Roster { id: string; name: string; kind: string; sector_id?: string; active: boolean; on_now: number }
export interface OnCallPeriod { id: string; user_id: string; from: string; to: string; capacity: number }

export type OverflowTrigger = string
export interface OverflowStep {
  position: number
  name?: string
  triggers: OverflowTrigger[]
  wait_seconds: number
  wait_business: boolean
  destination_kind: 'queue' | 'oncall' | 'agent' | 'sector' | 'hub' | 'flow'
  destination_id: string
  destination_queue_id?: string
  allow_closed: boolean
  allow_off_shift: boolean
}
export interface OverflowPolicy {
  id?: string
  queue_id?: string
  active: boolean
  max_steps: number
  on_exhausted: 'keep_waiting' | 'contingency' | 'manual'
  revision?: number
  steps: OverflowStep[]
}

export type SLAScope = 'tenant' | 'sector' | 'queue'
export interface SLAPolicy {
  scope_kind: SLAScope
  scope_id?: string
  first_response_seconds?: number
  queue_wait_seconds?: number
  assignment_seconds?: number
  resolution_seconds?: number
  risk_percent: number
  counting: 'wall' | 'business'
  active: boolean
  revision?: number
}
export interface SLAClock { conversation_id: string; metric: string; state: 'running' | 'at_risk' | 'breached'; started_at: string; risk_at: string; deadline_at: string; counting: string }
export interface QueueSLAMonitor { queue_id: string; counts: Record<string, number>; items: SLAClock[] }

export interface EligibilityVerdict {
  user_id: string
  name: string
  eligible: boolean
  reasons: string[]
  on_call: boolean
  workload: number
  capacity: number
  shift_source: string
}
export interface QueueSnapshot {
  queue_id: string
  at: string
  open: boolean
  presence_required: boolean
  eligible_count: number
  truncated?: boolean
  calendar_only: boolean
  agents: EligibilityVerdict[]
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

const h = () => ({ headers: authHeaders() })

export const sectorsAPI = {
  list: () => call<{ items: Sector[] }>(() => axios.get(`${base()}/sectors`, h())).then((r) => r.items),
  create: (name: string) => call<Sector>(() => axios.post(`${base()}/sectors`, { name }, h())),
  update: (id: string, patch: { name?: string; active?: boolean; calendar_id?: string | null }) =>
    call<unknown>(() => axios.patch(`${base()}/sectors/${id}`, patch, h())),
  setQueueSector: (queueId: string, sectorId: string | null) =>
    call<unknown>(() => axios.put(`${base()}/queues/${queueId}/sector`, { sector_id: sectorId }, h())),
}

export const calendarsAPI = {
  list: () => call<{ items: CalendarSummary[]; default_timezone: string; default_calendar_id?: string | null }>(() => axios.get(`${base()}/calendars`, h())),
  get: (id: string) => call<Calendar>(() => axios.get(`${base()}/calendars/${id}`, h())),
  create: (name: string, definition: CalendarSpec) => call<Calendar>(() => axios.post(`${base()}/calendars`, { name, definition }, h())),
  update: (id: string, name: string, definition: CalendarSpec, expectedRevision?: number) =>
    call<Calendar>(() => axios.put(`${base()}/calendars/${id}`, { name, definition, expected_revision: expectedRevision }, h())),
  remove: (id: string) => call<void>(() => axios.delete(`${base()}/calendars/${id}`, h())),
  addSuspension: (id: string, body: { from: string; to: string; reason?: string }) =>
    call<Suspension>(() => axios.post(`${base()}/calendars/${id}/suspensions`, body, h())),
  removeSuspension: (id: string, suspensionId: string) => call<void>(() => axios.delete(`${base()}/calendars/${id}/suspensions/${suspensionId}`, h())),
  setDefault: (calendarId: string | null) => call<unknown>(() => axios.put(`${base()}/settings/default-calendar`, { calendar_id: calendarId }, h())),
  setQueueCalendar: (queueId: string, calendarId: string | null) => call<unknown>(() => axios.put(`${base()}/queues/${queueId}/calendar`, { calendar_id: calendarId }, h())),
  availability: (queueId: string, at?: string) =>
    call<Availability>(() => axios.get(`${base()}/queues/${queueId}/availability`, { ...h(), params: at ? { at } : undefined })),
}

export const noticeAPI = {
  get: (queueId: string) => call<{ queue_id: string; text: string }>(() => axios.get(`${base()}/queues/${queueId}/closed-notice`, h())),
  put: (queueId: string, text: string) => call<{ queue_id: string; text: string }>(() => axios.put(`${base()}/queues/${queueId}/closed-notice`, { text }, h())),
}

export const openNoticeAPI = {
  get: (queueId: string) => call<{ queue_id: string; text: string }>(() => axios.get(`${base()}/queues/${queueId}/open-notice`, h())),
  put: (queueId: string, text: string) => call<{ queue_id: string; text: string }>(() => axios.put(`${base()}/queues/${queueId}/open-notice`, { text }, h())),
}

export const shiftsAPI = {
  list: (userId?: string) => call<{ items: AgentShift[] }>(() => axios.get(`${base()}/shifts`, { ...h(), params: userId ? { user_id: userId } : undefined })).then((r) => r.items),
  set: (body: { user_id: string; calendar_id: string | null; queue_id?: string | null; sector_id?: string | null }) =>
    call<unknown>(() => axios.put(`${base()}/shifts`, body, h())),
  rosters: () => call<{ items: Roster[] }>(() => axios.get(`${base()}/on-call/rosters`, h())).then((r) => r.items),
  createRoster: (body: { name: string; kind: string; sector_id?: string | null }) => call<Roster>(() => axios.post(`${base()}/on-call/rosters`, body, h())),
  updateRoster: (id: string, patch: { name?: string; active?: boolean }) => call<unknown>(() => axios.patch(`${base()}/on-call/rosters/${id}`, patch, h())),
  periods: (rosterId: string) => call<{ items: OnCallPeriod[] }>(() => axios.get(`${base()}/on-call/rosters/${rosterId}/periods`, h())).then((r) => r.items),
  addPeriod: (rosterId: string, body: { user_id: string; from: string; to: string; capacity: number }) =>
    call<OnCallPeriod>(() => axios.post(`${base()}/on-call/rosters/${rosterId}/periods`, body, h())),
  removePeriod: (rosterId: string, periodId: string) => call<void>(() => axios.delete(`${base()}/on-call/rosters/${rosterId}/periods/${periodId}`, h())),
}

export const overflowAPI = {
  get: (queueId: string) => call<{ policy: OverflowPolicy | null }>(() => axios.get(`${base()}/queues/${queueId}/overflow-policy`, h())).then((r) => r.policy),
  put: (queueId: string, policy: OverflowPolicy, expectedRevision?: number) =>
    call<{ policy: OverflowPolicy }>(() => axios.put(`${base()}/queues/${queueId}/overflow-policy`, { ...policy, expected_revision: expectedRevision }, h())).then((r) => r.policy),
  remove: (queueId: string) => call<void>(() => axios.delete(`${base()}/queues/${queueId}/overflow-policy`, h())),
  eligibility: (queueId: string) => call<QueueSnapshot>(() => axios.get(`${base()}/queues/${queueId}/eligibility`, h())),
}

export interface HubContract { contract_id: string; hub_name: string; status: string; valid_from: string; valid_until?: string }
export const hubContractsAPI = {
  list: () => call<{ items: HubContract[] }>(() => axios.get(`${base()}/hub-contracts`, h())).then((r) => r.items),
}

export const slaAPI = {
  list: () => call<{ items: SLAPolicy[] }>(() => axios.get(`${base()}/sla-policies`, h())).then((r) => r.items),
  put: (policy: SLAPolicy, expectedRevision?: number) => call<SLAPolicy>(() => axios.put(`${base()}/sla-policies`, { ...policy, expected_revision: expectedRevision }, h())),
  remove: (scope: SLAScope, scopeId?: string) => call<void>(() => axios.delete(`${base()}/sla-policies`, { ...h(), params: { scope_kind: scope, scope_id: scopeId } })),
  monitor: (queueId: string) => call<QueueSLAMonitor>(() => axios.get(`${base()}/queues/${queueId}/sla`, h())),
}

// O servidor responde texto simples; a mensagem de validação (400) nomeia a regra que falhou e não traz dados de outra empresa, então
// pode ser mostrada. Os demais códigos viram frases que a pessoa consegue agir.
export function attendanceErrorMessage(err: any, fallback = 'Não foi possível concluir a alteração.'): string {
  const status = err?.response?.status
  const text = typeof err?.response?.data === 'string' ? err.response.data.trim() : ''
  if (status === 403) return 'Você não tem permissão para esta alteração.'
  if (status === 404) return 'Este item não existe mais. Atualize a página.'
  if (status === 409) {
    if (/changed|stale|revision/i.test(text)) return 'Outra pessoa alterou isto enquanto você editava. Recarregue e aplique de novo.'
    if (/in use|usado|still/i.test(text)) return 'Este item ainda está em uso. Troque onde ele é usado antes de remover.'
    return 'Já existe um item com esse nome, ou ele foi alterado por outra pessoa.'
  }
  if (status === 400 && text) return `Confira os dados: ${text}`
  return fallback
}

// Onde a conversa está e por quê (GET .../inbox/conversations/{id}/routing). Só nomes e estados: nunca conteúdo de mensagem.
export interface ConversationRouting {
  conversation_id: string
  status: string
  queue?: { id: string; name: string }
  sector?: { id: string; name: string }
  assigned?: { user_id: string; name: string; at?: string }
  engine?: { state: string; last_decision: string; episode: number; step: number; hops: number; entered_queue_at: string; waiting_seconds?: number; next_attempt_at?: string }
  schedule?: Availability
  sla: { metric: string; state: string; started_at: string; risk_at: string; deadline_at: string; counting: string; met_at?: string; breached_at?: string }[]
  history: { at: string; trigger: string; decision: string; reason: string; destination?: string; destination_label?: string; attempts: number }[]
}

export const routingAPI = {
  conversation: (conversationId: string) =>
    call<ConversationRouting>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/routing`, h())),
}
