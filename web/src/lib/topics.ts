import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors the topic payloads of internal/intelligence/adapters/http.go (ADR-0017). A topic is the LOGICAL subject being
// handled; the conversation stays the physical thread. Everything here is read/act through the API: the UI never decides
// authorization (the backend refuses what the user cannot do) and never sends a message on behalf of the copilot.

export type TopicStatus = 'open' | 'resolved' | 'archived'

export interface Topic {
  id: string
  title: string
  status: TopicStatus
  privacy_policy: string
  source: string
  last_activity_at: string
  message_count?: number
  ticket_count?: number
  merged_into_topic_id?: string
  split_from_topic_id?: string
}

export interface TopicMessage {
  id: string
  direction: 'inbound' | 'outbound'
  message_type: string
  body?: string
  created_at: string
  relation: 'primary' | 'secondary' | 'supporting' | 'ambiguous'
  decision_source: string
  confidence?: number
}

export type SummaryStatus = 'ai_inferred' | 'agent_confirmed' | 'customer_confirmed' | 'corrected' | 'superseded'

export interface TopicSummary {
  id: string
  version: number
  summary_text: string
  status: SummaryStatus
  authored_by: 'machine' | 'agent'
  created_at: string
}

export interface AmbiguityCandidate {
  topic_id: string
  title?: string
  score?: number
}

export interface Ambiguity {
  id: string
  message_id: string
  kind: 'conversation' | 'group'
  candidates: AmbiguityCandidate[]
  created_at: string
}

export type TicketAction = 'none' | 'adopt_active' | 'share_active' | 'create' | 'needs_agent'

export interface TicketAdvice {
  action: TicketAction
  ticket_id?: string
  reason: string
  allowed_actions: Array<'adopt_active' | 'share_active' | 'create'>
}

export interface TopicTicket {
  id: string
  status: string
  priority: string
  subject: string
  relation: string
}

export interface CopilotDraft {
  reply: string
  missing_info: string[]
  needs_human: boolean
  warnings: string[]
  sent: false
}

export interface HandoffInvite {
  handoff: { id: string; status: string; expires_at: string }
  token: string
  instructions: string
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`
const cfg = () => ({ headers: authHeaders() })

// Every call funnels errors through here: an expired session logs out; everything else is returned as an HTTP status so
// each section can degrade on its own (a feature that is switched off answers 404 and simply disappears).
async function call<T>(fn: () => Promise<{ data: T; status?: number }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err: any) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw new TopicsError(err?.response?.status ?? 0)
  }
}

// Company context of a subject (ADR-0018). It belongs to the TOPIC, never to the conversation. A contact that belongs to
// several companies is never resolved by guessing: the status is needs_choice and a person picks.
export interface TopicAccountRef {
  account_id: string
  name: string
  // topic_link: a person chose it (persisted). ticket / sole_company: derived on read, never stored.
  source: 'topic_link' | 'ticket' | 'sole_company'
  persisted: boolean
  linked_to_contact: boolean
}
export interface TopicAccountCandidate {
  account_id: string
  name: string
  relationship_type: string
  // The contact's own preference, NOT an answer: primary is not exclusive.
  contact_primary: boolean
}
export interface TopicAccountContext {
  status: 'resolved' | 'needs_choice' | 'none'
  primary?: TopicAccountRef
  related: TopicAccountRef[]
  candidates: TopicAccountCandidate[]
}

export class TopicsError extends Error {
  status: number
  constructor(status: number) {
    super(`topics request failed (${status})`)
    this.status = status
  }
}

export const topicsAPI = {
  accountContext: (topicId: string) => call<TopicAccountContext>(() => axios.get(`${base()}/topics/${topicId}/account-context`, cfg())),
  linkAccount: (topicId: string, accountId: string, relation: 'primary' | 'related') =>
    call<TopicAccountContext>(() => axios.post(`${base()}/topics/${topicId}/accounts`, { account_id: accountId, relation }, cfg())),
  unlinkAccount: (topicId: string, accountId: string) =>
    call<TopicAccountContext>(() => axios.delete(`${base()}/topics/${topicId}/accounts/${accountId}`, cfg())),
  listForConversation: (conversationId: string) =>
    call<{ items: Topic[] }>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/topics`, cfg())).then((d) => d.items ?? []),
  listForContact: (contactId: string) =>
    call<{ items: Topic[] }>(() => axios.get(`${base()}/contacts/${contactId}/topics`, cfg())).then((d) => d.items ?? []),
  create: (conversationId: string, title: string) =>
    call<Topic>(() => axios.post(`${base()}/inbox/conversations/${conversationId}/topics`, { title }, cfg())),
  setStatus: (topicId: string, status: 'open' | 'resolved') =>
    call<Topic>(() => axios.patch(`${base()}/topics/${topicId}`, { status }, cfg())),
  messages: (topicId: string) =>
    call<{ items: TopicMessage[]; next_cursor?: string }>(() => axios.get(`${base()}/topics/${topicId}/messages?limit=20`, cfg())).then((d) => d.items ?? []),
  tickets: (topicId: string) =>
    call<{ items: TopicTicket[] }>(() => axios.get(`${base()}/topics/${topicId}/tickets`, cfg())).then((d) => d.items ?? []),

  summaries: (topicId: string) =>
    call<{ items: TopicSummary[] }>(() => axios.get(`${base()}/topics/${topicId}/summaries`, cfg())).then((d) => d.items ?? []),
  generateSummary: (topicId: string) => call<TopicSummary>(() => axios.post(`${base()}/topics/${topicId}/summary/generate`, {}, cfg())),
  confirmSummary: (topicId: string) => call<TopicSummary>(() => axios.post(`${base()}/topics/${topicId}/summary/confirm`, {}, cfg())),
  correctSummary: (topicId: string, text: string) =>
    call<TopicSummary>(() => axios.post(`${base()}/topics/${topicId}/summary/correct`, { summary_text: text }, cfg())),

  ambiguities: (conversationId: string) =>
    call<{ items: Ambiguity[] }>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/ambiguities`, cfg())).then((d) => d.items ?? []),
  resolveAmbiguity: (id: string, choice: { topic_id: string } | { new_topic_title: string }) =>
    call<unknown>(() => axios.post(`${base()}/ambiguities/${id}/resolve`, choice, cfg())),

  ticketAdvice: (topicId: string) => call<TicketAdvice>(() => axios.get(`${base()}/topics/${topicId}/ticket-policy`, cfg())),
  applyTicketAction: (topicId: string, action: 'adopt_active' | 'share_active' | 'create') =>
    call<{ ticket_id: string; relation: string; created: boolean }>(() => axios.post(`${base()}/topics/${topicId}/ticket-policy/apply`, { action }, cfg())),

  suggestReply: (topicId: string) => call<CopilotDraft>(() => axios.post(`${base()}/topics/${topicId}/copilot/suggest-reply`, {}, cfg())),
  createHandoff: (topicId: string) => call<HandoffInvite>(() => axios.post(`${base()}/topics/${topicId}/handoffs`, {}, cfg())),
}

export const summaryStatusLabel: Record<SummaryStatus, string> = {
  ai_inferred: 'Rascunho da IA',
  agent_confirmed: 'Confirmado pelo atendente',
  customer_confirmed: 'Confirmado pelo cliente',
  corrected: 'Corrigido pelo atendente',
  superseded: 'Substituído',
}

export const ticketActionLabel: Record<'adopt_active' | 'share_active' | 'create', string> = {
  adopt_active: 'Vincular ao chamado ativo',
  share_active: 'Relacionar ao chamado ativo',
  create: 'Abrir chamado para este assunto',
}

export const copilotWarningLabel: Record<string, string> = {
  link_not_in_context: 'Contém um link que não está na conversa',
  email_not_in_context: 'Contém um e-mail que não está na conversa',
  phone_not_in_context: 'Contém um telefone que não está na conversa',
  number_not_in_context: 'Contém um número que não está na conversa',
  amount_mentioned: 'Cita um valor em reais',
  claims_action_done: 'Afirma que algo já foi feito',
  contains_promise: 'Contém uma promessa ou garantia',
}
