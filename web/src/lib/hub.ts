import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// Mirrors the Hub schemas in contracts/openapi/omnira-v1.yaml (GET /hubs, /hubs/{id}/inbox, /hubs/{id}/inbox/{item}).
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
  access: { source: string; hub_id?: string; grant_id?: string }
  conversation: { id: string; status: string; title?: string; created_at: string }
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
