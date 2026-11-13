import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// CONTEXT.2C: the server's own aggregation over CONTEXT.2B's structured analyses (internal/intelligence/adapters/
// context_view_http.go) — never the model's own words taken as fact, and never something the UI decided on its own.
// Reading needs conversation.read (checked by the route) and topic.read (checked again live for a Hub-delegated
// actor too); the feature is 404-shaped away entirely while OMNIRA_CONTEXT_INTELLIGENCE_V2_ENABLED is off.

export type FactKind = 'need' | 'observation' | 'commitment' | 'suggested_action' | 'possible_resolution' | 'risk'
export type FactStatus = 'open' | 'confirmed' | 'dismissed'
export type AnalysisState = 'none' | 'ok' | 'empty' | 'failed'

export interface ContextFact {
  id: string
  kind: FactKind
  text: string
  truth_level: string
  status: FactStatus
  evidence_message_ids: string[]
  created_at: string
}

export interface ContextTopic {
  topic_id: string
  title: string
  status: string
  facts: ContextFact[]
}

export interface ContextView {
  analysis_state: AnalysisState
  updated_at?: string
  summary: string
  stale: boolean
  coverage: { analyzed_message_count: number; total_inbound_message_count: number; last_analyzed_message_at?: string }
  topics: ContextTopic[]
  pending_actions: ContextFact[]
  suggestions: ContextFact[]
  evidence_refs: string[]
  needs_customer_account_selection?: boolean
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`
const cfg = () => ({ headers: authHeaders() })

export class ContextViewError extends Error {
  status: number
  constructor(status: number) {
    super(`context-view request failed (${status})`)
    this.status = status
  }
}

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err: any) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw new ContextViewError(err?.response?.status ?? 0)
  }
}

export const contextViewAPI = {
  get: (conversationId: string) => call<ContextView>(() => axios.get(`${base()}/inbox/conversations/${conversationId}/context-view`, cfg())),
  refresh: (conversationId: string) =>
    call<{ forced: boolean }>(() => axios.post(`${base()}/inbox/conversations/${conversationId}/context-analysis/refresh`, {}, cfg())),
}

export const factKindLabel: Record<FactKind, string> = {
  need: 'Necessidade',
  observation: 'Observação',
  commitment: 'Compromisso assumido',
  suggested_action: 'Sugestão',
  possible_resolution: 'Possível resolução',
  risk: 'Risco',
}
