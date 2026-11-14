import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'
import type { CustomerAccount, DirectoryCompany, RelationshipType } from './contacts'

// Mirrors the group payloads in internal/groups/adapters/http.go (ADR-0015). Groups are a separate
// area from conversations: read-only, no assignee/queue/SLA, and no phone number is ever served.
export type GroupMessageType = 'text' | 'image' | 'video' | 'audio' | 'document' | 'sticker' | 'location' | 'other'

// Manual, closed-list work-purpose tag an administrator picks per group (migration 000178;
// 'partner'/'supplier' added by migration 000190). Never guessed from participants — that is a
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

// A customer_account named just for display — used for a group's additional (non-primary) clients.
export interface AccountRef {
  id: string
  name: string
}

export interface Group {
  id: string
  name: string
  enabled: boolean
  purpose: GroupPurpose
  account_id: string | null
  account_name: string | null
  // Every OTHER client this group also concerns (ADR-0059 addendum 8 — e.g. a "swap"/peering group between
  // two of the tenant's own clients). Never the primary above; always present, empty when there are none.
  additional_accounts: AccountRef[]
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
  // ADR-0015 addendum: who wrote it, identified WITHOUT a phone (channel_participants, never contacts —
  // author_jid is often an opaque @lid). Null only when the message carries no usable author id.
  participant_id: string | null
  company_name: string | null
  company_role: RelationshipType | null
  // ADR-0015 addendum 3: the linked company is itself a client of the Client the group represents (ex.:
  // Uplink, cliente da 4WNET), not the Client's own staff. Null exactly when company_name is null.
  is_downstream_client: boolean | null
  // ADR-0015 addendum 2: the vision step's already-done description of this message's own image/PDF. Null
  // while pending, on a text message, or when analysis never completed — never guessed.
  image_caption: string | null
}

/** Where an <img> loads a group message's cleared image/PDF from (ADR-0015 addendum 2). Groups have no
 * Hub-delegated read path into message content, so unlike messageMediaUrl this never branches on acting. */
export function groupMessageMediaUrl(tenantId: string, groupId: string, messageId: string): string {
  return `${API_BASE}/tenants/${tenantId}/groups/${groupId}/messages/${messageId}/media`
}

// ADR-0015 addendum: one participant ever seen writing in a group. The provider's raw JID/phone
// (external_participant_id) is never served — same rule as the rest of the group-messages area.
export interface GroupParticipant {
  id: string
  display_name: string
  alias: string | null
  company_account_id: string | null
  company_name: string | null
  company_role: RelationshipType | null
  // ADR-0015 addendum 3: the linked company is itself a client of the Client (ex.: Uplink, cliente da
  // 4WNET), not the Client's own staff/representative. Always false while no company is linked.
  is_downstream_client: boolean
  message_count: number
  last_seen_at: string
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

// One active CRM client (customer_accounts) with how many of the tenant's ENABLED groups concern it,
// counting a group whether it is the primary (account_id) or one of its OTHER clients (additional_accounts) —
// never double-counted. Powers the "Organização" browser in GroupsPage: a client-first way to navigate groups,
// never an Atendimento/ticket count (that feature doesn't exist for groups yet).
export interface AccountGroupsSummary {
  account_id: string
  account_name: string
  group_count: number
}

export const groupsAPI = {
  list: (accountId?: string, connectionId?: string) =>
    call<{ items: Group[] }>(() =>
      axios.get(base(), { headers: authHeaders(), params: { ...(accountId ? { account_id: accountId } : {}), ...(connectionId ? { connection_id: connectionId } : {}) } }),
    ).then((r) => r.items),
  accountsSummary: () =>
    call<{ items: AccountGroupsSummary[] }>(() => axios.get(`${base()}/accounts-summary`, { headers: authHeaders() })),
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
  // ADR-0059 addendum 8: a group may ALSO concern other clients besides its one primary (account_id) —
  // e.g. a "swap"/peering group between two of the tenant's own clients. These never touch account_id.
  addAdditionalAccount: (id: string, ref: GroupAccountRef) =>
    call<Group>(() =>
      axios.post(`${base()}/${id}/accounts`, 'accountId' in ref ? { account_id: ref.accountId } : { directory_company_id: ref.directoryCompanyId }, { headers: authHeaders() }),
    ),
  removeAdditionalAccount: (id: string, accountId: string) =>
    call<Group>(() => axios.delete(`${base()}/${id}/accounts/${accountId}`, { headers: authHeaders() })),
  // Explicit-tenant variants (Hub Organizer): same endpoints as above, called with the catalog entry's
  // OWN tenant instead of the browser's currently-selected one (getTenantId()) — the Hub Organizer shows
  // groups across whichever tenants delegated 'groups', never just the session's own. Deliberately plain
  // tenant-scoped calls (authHeaders() only, no X-Omnira-Acting-As/DelegatedTarget): this endpoint has no
  // delegated-serving support at all (internal/groups/adapters/http.go's session check is a flat
  // memberships+group.manage lookup), so it authorizes a real tenant member exactly the same whether the
  // browser tab is on the tenant's own Groups page or on the Hub Organizer — nothing new is granted.
  linkAccountFor: (tenantId: string, id: string, ref: GroupAccountRef) =>
    call<Group>(() =>
      axios.put(`${API_BASE}/tenants/${tenantId}/groups/${id}/account`, 'accountId' in ref ? { account_id: ref.accountId } : { directory_company_id: ref.directoryCompanyId }, {
        headers: authHeaders(),
      }),
    ),
  unlinkAccountFor: (tenantId: string, id: string) =>
    call<Group>(() => axios.delete(`${API_BASE}/tenants/${tenantId}/groups/${id}/account`, { headers: authHeaders() })),
  setPurposeFor: (tenantId: string, id: string, purpose: GroupPurpose) =>
    call<Group>(() => axios.patch(`${API_BASE}/tenants/${tenantId}/groups/${id}`, { purpose }, { headers: authHeaders() })),
  addAdditionalAccountFor: (tenantId: string, id: string, ref: GroupAccountRef) =>
    call<Group>(() =>
      axios.post(`${API_BASE}/tenants/${tenantId}/groups/${id}/accounts`, 'accountId' in ref ? { account_id: ref.accountId } : { directory_company_id: ref.directoryCompanyId }, {
        headers: authHeaders(),
      }),
    ),
  removeAdditionalAccountFor: (tenantId: string, id: string, accountId: string) =>
    call<Group>(() => axios.delete(`${API_BASE}/tenants/${tenantId}/groups/${id}/accounts/${accountId}`, { headers: authHeaders() })),
  crmDirectoryFor: (tenantId: string) =>
    call<{ items: DirectoryCompany[] }>(() => axios.get(`${API_BASE}/tenants/${tenantId}/crm/companies`, { headers: authHeaders() })),
  localAccountsFor: (tenantId: string, q?: string) =>
    call<{ items: CustomerAccount[] }>(() =>
      axios.get(`${API_BASE}/tenants/${tenantId}/accounts`, { headers: authHeaders(), params: { status: 'active', ...(q ? { q } : {}) } }),
    ),
  messages: (id: string, cursor?: string, limit = 100) =>
    call<GroupMessagePage>(() =>
      axios.get(`${base()}/${id}/messages`, { headers: authHeaders(), params: { limit, ...(cursor ? { cursor } : {}) } }),
    ),
  deleteHistory: (id: string) => call<{ deleted: number }>(() => axios.delete(`${base()}/${id}/messages`, { headers: authHeaders() })),
  // ADR-0015 addendum: who writes in a group (alias/company/role), identified without a phone.
  participants: (id: string) => call<{ items: GroupParticipant[] }>(() => axios.get(`${base()}/${id}/participants`, { headers: authHeaders() })),
  setParticipantAlias: (participantId: string, alias: string) =>
    call<GroupParticipant>(() => axios.put(`${base()}/participants/${participantId}/details`, { alias }, { headers: authHeaders() })),
  // .../company/link (not .../company): the backend's net/http.ServeMux refuses the shorter path as an
  // unresolvable conflict with DELETE .../{id}/accounts/{account_id} — see RegisterGroupHandlers.
  setParticipantCompany: (participantId: string, ref: GroupAccountRef, role?: RelationshipType, isDownstreamClient = false) =>
    call<GroupParticipant>(() =>
      axios.put(
        `${base()}/participants/${participantId}/company/link`,
        {
          ...('accountId' in ref ? { account_id: ref.accountId } : { directory_company_id: ref.directoryCompanyId }),
          ...(role ? { role } : {}),
          is_downstream_client: isDownstreamClient,
        },
        { headers: authHeaders() },
      ),
    ),
  unlinkParticipantCompany: (participantId: string) =>
    call<GroupParticipant>(() => axios.delete(`${base()}/participants/${participantId}/company/link`, { headers: authHeaders() })),
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
