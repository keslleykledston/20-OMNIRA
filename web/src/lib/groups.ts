import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors the group payloads in internal/groups/adapters/http.go (ADR-0015). Groups are a separate
// area from conversations: read-only, no assignee/queue/SLA, and no phone number is ever served.
export type GroupMessageType = 'text' | 'image' | 'video' | 'audio' | 'document' | 'sticker' | 'location' | 'other'

// Manual, closed-list work-purpose tag an administrator picks per group (migration 000176;
// 'partner'/'supplier' added by migration 000188). Never guessed from participants — that is a
// different, automatic classification the backend keeps to itself.
export type GroupPurpose =
  | 'client_management'
  | 'client_support'
  | 'internal_management'
  | 'internal_team'
  | 'partner'
  | 'supplier'
  | 'other'

export interface GroupLastMessage {
  author_name: string
  from_me: boolean
  message_type: GroupMessageType
  preview: string
}

export interface Group {
  id: string
  name: string
  enabled: boolean
  purpose: GroupPurpose
  account_id: string | null
  account_name: string | null
  connection_id: string
  connection_account: string
  ai_observer_enabled: boolean
  // ADR-0058: at most one group per tenant may be true — the one exception to "nothing is sent to a group".
  // A real WhatsApp message goes here when a conversation's SLA breaches.
  is_alert_channel: boolean
  last_message_at: string | null
  last_message: GroupLastMessage | null
}

// A suggestion the AI observer left on one message (ADR-0055) — only ever a suggestion for a human to
// review; the backend never acts on it and never sends anything anywhere.
export type GroupSuggestionCategory = 'request' | 'complaint' | 'question' | 'info' | 'other'

export interface GroupMessageSuggestion {
  category: GroupSuggestionCategory
  summary: string
  suggested_action: string
  confidence: number
}

export interface AvailableGroup {
  provider_group_id: string
  name: string
  participant_count: number
  enabled: boolean
  group_id: string | null
}

export interface AvailableGroupPage {
  items: AvailableGroup[]
  count: number
  total: number
  connection_id: string
}

export interface GroupMessage {
  id: string
  author_name: string
  from_me: boolean
  message_type: GroupMessageType
  body: string
  sent_at: string
  suggestion: GroupMessageSuggestion | null
}

export interface GroupMessagePage {
  items: GroupMessage[]
  has_more: boolean
  next_cursor?: string
}

// Either a local customer_accounts id, or a company picked straight from the tenant's own CRM directory
// (ADR-0059 addendum 3) — mutually exclusive, same shape the backend's accountRefDTO-style XOR expects.
export type GroupAccountRef = { accountId: string } | { directoryCompanyId: string }

const base = () => `${API_BASE}/tenants/${getTenantId()}/groups`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const groupsAPI = {
  list: (accountId?: string, connectionId?: string) =>
    call<{ items: Group[] }>(() =>
      axios.get(base(), { headers: authHeaders(), params: { ...(accountId ? { account_id: accountId } : {}), ...(connectionId ? { connection_id: connectionId } : {}) } }),
    ).then((r) => r.items),
  available: (q: string, limit = 50, connectionId?: string) =>
    call<AvailableGroupPage>(() =>
      axios.get(`${base()}/available`, { headers: authHeaders(), params: { ...(q ? { q } : {}), limit, ...(connectionId ? { connection_id: connectionId } : {}) } }),
    ),
  enable: (providerGroupId: string, connectionId?: string) =>
    call<Group>(() =>
      axios.post(base(), { provider_group_id: providerGroupId, ...(connectionId ? { connection_id: connectionId } : {}) }, { headers: authHeaders() }),
    ),
  setEnabled: (id: string, enabled: boolean) =>
    call<Group>(() => axios.patch(`${base()}/${id}`, { enabled }, { headers: authHeaders() })),
  setPurpose: (id: string, purpose: GroupPurpose) =>
    call<Group>(() => axios.patch(`${base()}/${id}`, { purpose }, { headers: authHeaders() })),
  setAIObserverEnabled: (id: string, aiObserverEnabled: boolean) =>
    call<Group>(() => axios.patch(`${base()}/${id}`, { ai_observer_enabled: aiObserverEnabled }, { headers: authHeaders() })),
  setAlertChannel: (id: string, isAlertChannel: boolean) =>
    call<Group>(() => axios.patch(`${base()}/${id}`, { is_alert_channel: isAlertChannel }, { headers: authHeaders() })),
  // ADR-0059 addendum 3: the account is named EITHER by a local account id (already registered in this tenant) OR
  // by a directory_company_id (a company read straight from the tenant's own CRM directory, GET /crm/companies) —
  // for a groups-only tenant whose customer_accounts starts empty, so there is nothing local to search yet.
  linkAccount: (id: string, ref: GroupAccountRef) =>
    call<Group>(() =>
      axios.put(`${base()}/${id}/account`, 'accountId' in ref ? { account_id: ref.accountId } : { directory_company_id: ref.directoryCompanyId }, {
        headers: authHeaders(),
      }),
    ),
  unlinkAccount: (id: string) => call<Group>(() => axios.delete(`${base()}/${id}/account`, { headers: authHeaders() })),
  messages: (id: string, cursor?: string, limit = 100) =>
    call<GroupMessagePage>(() =>
      axios.get(`${base()}/${id}/messages`, { headers: authHeaders(), params: { limit, ...(cursor ? { cursor } : {}) } }),
    ),
  deleteHistory: (id: string) => call<{ deleted: number }>(() => axios.delete(`${base()}/${id}/messages`, { headers: authHeaders() })),
}

export function groupErrorMessage(err: unknown): string {
  const response = (err as { response?: { status?: number; data?: unknown } })?.response
  switch (response?.status) {
    case 403:
      return 'Você não tem permissão para esta ação nos grupos.'
    case 404:
      return 'Grupo não encontrado na conta do WhatsApp.'
    case 409: {
      // ADR-0058: the server's plain-text body already says exactly which alert-channel rule was hit; the
      // generic fallback below is for the older 409 (no active WhatsApp connection to list groups).
      const body = typeof response.data === 'string' ? response.data : ''
      if (body.includes('already the alert channel')) return 'Só pode haver um grupo canal de alerta por conta — desative o outro primeiro.'
      if (body.includes('cannot be set on a group linked') || body.includes('cannot be linked to a customer account'))
        return 'Um grupo de cliente não pode ser o canal de alerta (e vice-versa).'
      return 'Não há uma conexão ativa do WhatsApp para listar os grupos.'
    }
    case 422: {
      const body = typeof response.data === 'string' ? response.data : ''
      if (body.includes('directory')) return 'Essa empresa não foi encontrada ou está inativa no CRM.'
      return 'Não foi possível concluir: dados inválidos.'
    }
    case 502: {
      const body = typeof response.data === 'string' ? response.data : ''
      if (body.includes('directory')) return 'O CRM não respondeu. Tente de novo em instantes.'
      return 'O WhatsApp não respondeu. Tente de novo em instantes.'
    }
    case 503:
      return 'O diretório de empresas do CRM não está disponível agora.'
    default:
      return 'Não foi possível concluir. Tente novamente.'
  }
}
