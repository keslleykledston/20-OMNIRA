import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// Mirrors the Hub schemas in contracts/openapi/omnira-v1.yaml (GET /hubs, /hubs/{id}/inbox, /hubs/{id}/inbox/{item}, POST .../claim and .../messages).
// The Hub API is feature-flagged on the server: when it is off the routes do not exist and /hubs answers 404.

export interface HubSummary {
  id: string
  name: string
  role: 'hub_agent' | 'hub_admin'
}

export interface HubInboxItem {
  id: string
  tenant_id: string
  tenant_name: string
  conversation_id: string
  queue_id?: string
  customer_name: string
  channel: string
  status: string
  priority: 'low' | 'normal' | 'high' | 'urgent' | string
  sla_due_at?: string
  last_activity_at?: string
  unread_count: number
}

export interface HubInboxPage {
  items: HubInboxItem[]
  has_more: boolean
  next_cursor?: string
  count: number
  limit: number
}

export interface HubMessage {
  id: string
  direction: 'inbound' | 'outbound'
  message_type: string
  body: string
  status: string
  created_at: string
}

export interface HubItemDetail {
  item: HubInboxItem
  tenant: { id: string; name: string }
  access: { source: string; hub_id?: string; grant_id?: string; can_reply?: boolean }
  // assignment is relative to the caller: the server never names another operator
  conversation: { id: string; status: string; title?: string; created_at: string; assignment?: 'none' | 'me' | 'other' }
  messages: HubMessage[]
}

const enc = encodeURIComponent

export const hubAPI = {
  // [] when the Hub is not available (404: flag off); any other failure throws so the page can say so.
  mine: async (): Promise<HubSummary[]> => {
    try {
      const r = await axios.get<{ items: HubSummary[] }>(`${API_BASE}/hubs`, { headers: authHeaders() })
      return r.data.items ?? []
    } catch (err) {
      if ((err as { response?: { status?: number } })?.response?.status === 404) return []
      throw err
    }
  },
  inbox: (hubId: string, cursor?: string): Promise<HubInboxPage> =>
    axios
      .get<HubInboxPage>(`${API_BASE}/hubs/${enc(hubId)}/inbox`, { headers: authHeaders(), params: { limit: 30, ...(cursor ? { cursor } : {}) } })
      .then((r) => r.data),
  item: (hubId: string, itemId: string): Promise<HubItemDetail> =>
    axios.get<HubItemDetail>(`${API_BASE}/hubs/${enc(hubId)}/inbox/${enc(itemId)}`, { headers: authHeaders() }).then((r) => r.data),
}

export interface HubWriteTenant {
  id: string
  name: string
}

export interface HubClaimResult {
  item_id: string
  conversation_id: string
  changed: boolean
  tenant: HubWriteTenant
}

export interface HubReplyResult {
  id: string
  conversation_id: string
  status: string
  tenant: HubWriteTenant
}

// Writes. `expectedTenantId` is the company the screen is SHOWING. It authorizes nothing: the server derives the tenant
// from the item and answers 409 if the two differ, so a stale or confused screen can never speak as another company.
export const hubWriteAPI = {
  claim: (hubId: string, itemId: string, expectedTenantId: string): Promise<HubClaimResult> =>
    axios
      .post<HubClaimResult>(`${API_BASE}/hubs/${enc(hubId)}/inbox/${enc(itemId)}/claim`, { expected_tenant_id: expectedTenantId }, { headers: authHeaders() })
      .then((r) => r.data),
  reply: (hubId: string, itemId: string, expectedTenantId: string, text: string, idempotencyKey: string): Promise<HubReplyResult> =>
    axios
      .post<HubReplyResult>(
        `${API_BASE}/hubs/${enc(hubId)}/inbox/${enc(itemId)}/messages`,
        { expected_tenant_id: expectedTenantId, text },
        { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey } },
      )
      .then((r) => r.data),
}

// The write routes answer with plain text. One sentence per case, in the operator's words.
export function describeHubWriteError(err: unknown): string {
  const e = err as { response?: { status?: number; data?: unknown } }
  const status = e?.response?.status
  const body = typeof e?.response?.data === 'string' ? e.response.data : ''
  switch (status) {
    case 403:
      return 'Seu acesso a esta empresa é somente leitura.'
    case 404:
      return 'Esta conversa não está mais disponível para você: o acesso pode ter sido encerrado.'
    case 409:
      if (body.includes('another agent')) return 'Esta conversa já está com outro operador.'
      if (body.includes('claim')) return 'Assuma esta conversa antes de responder.'
      if (body.includes('window')) return 'Janela de 24 h fechada: só mensagem de template até o cliente escrever de novo.'
      if (body.includes('finalized')) return 'Este atendimento foi finalizado.'
      if (body.includes('company')) return 'A empresa exibida não é a desta conversa. Recarregue a tela.'
      return 'A conversa mudou, tente novamente.'
    case 400:
    case 422:
      return body || 'Mensagem inválida.'
    default:
      return 'Não foi possível concluir a ação.'
  }
}
