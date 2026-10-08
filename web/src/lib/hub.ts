import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders } from './session'

// Mirrors the Hub schemas in contracts/openapi/omnira-v1.yaml (GET /hubs, /hubs/{id}/inbox, /hubs/{id}/inbox/{item}, POST .../claim and .../messages).
// The Hub API is feature-flagged on the server: when it is off the routes do not exist and /hubs answers 404.

export interface HubSummary {
  id: string
  name: string
  role: 'hub_agent' | 'hub_admin'
  // Only whether to offer the company screen. The server decides every request again.
  can_manage_companies?: boolean
  // Same for the Access panel (people and permissions): offered to hub admins when the server mounts it.
  can_manage_access?: boolean
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

// A company the signed-in person may serve through the hub; feeds the inbox filter. Describes, never authorizes.
export interface HubCompanyOption {
  id: string
  name: string
}

export interface HubInboxPage {
  items: HubInboxItem[]
  companies?: HubCompanyOption[]
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
  // `companies` only NARROWS the caller's authorized view (empty = all of it); the server never widens because of it.
  inbox: (hubId: string, cursor?: string, companies: string[] = [], limit = 30): Promise<HubInboxPage> =>
    axios
      .get<HubInboxPage>(`${API_BASE}/hubs/${enc(hubId)}/inbox`, {
        headers: authHeaders(),
        params: { limit, ...(cursor ? { cursor } : {}), ...(companies.length ? { companies: companies.join(',') } : {}) },
      })
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

// ---- Control plane (ADR-0038): companies of a Hub. Only an active platform operator who administers the hub is answered;
// everyone else gets 404. The API is behind OMNIRA_HUB_ADMIN_API_ENABLED.

export interface HubCapability {
  key: string
  label: string
  description: string
  gates: string
}

export interface HubCompany {
  id: string
  legal_name: string
  trade_name: string
  display_name: string
  status: 'active' | 'suspended' | string
  contract_status: string
  capabilities: Record<string, boolean>
  channels: number
  integrations: number
  open_conversations: number
  agents: number
  created_at: string
}

export interface HubCompanyList {
  items: HubCompany[]
  capabilities: HubCapability[]
}

export interface NewCompany {
  legal_name: string
  trade_name?: string
  tax_id?: string
  initial_admin_email?: string
}

export const hubAdminAPI = {
  companies: (hubId: string): Promise<HubCompanyList> =>
    axios.get<HubCompanyList>(`${API_BASE}/hubs/${enc(hubId)}/companies`, { headers: authHeaders() }).then((r) => r.data),
  create: (hubId: string, body: NewCompany, idempotencyKey: string): Promise<HubCompany> =>
    axios
      .post<HubCompany>(`${API_BASE}/hubs/${enc(hubId)}/companies`, body, { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey } })
      .then((r) => r.data),
  update: (hubId: string, tenantId: string, body: { status?: 'active' | 'suspended'; capabilities?: Record<string, boolean> }): Promise<HubCompany> =>
    axios.patch<HubCompany>(`${API_BASE}/hubs/${enc(hubId)}/companies/${enc(tenantId)}`, body, { headers: authHeaders() }).then((r) => r.data),
}

export function describeHubAdminError(err: unknown): string {
  const e = err as { response?: { status?: number; data?: unknown } }
  const status = e?.response?.status
  const body = typeof e?.response?.data === 'string' ? e.response.data.replace(/^companies: invalid request: /, '').trim() : ''
  switch (status) {
    case 404:
      return 'Você não tem permissão para gerenciar empresas neste Hub (ou a empresa não é deste Hub).'
    case 400:
      return 'Pedido inválido. Recarregue a tela e tente de novo.'
    case 422:
      return body || 'Os dados informados não foram aceitos.'
    default:
      return 'Não foi possível concluir a ação.'
  }
}

// ---- Access panel (ADR-0039): instances (companies), their administrators, the hub's agents and what each may do where.
// Only an active admin of the hub is answered; everyone else gets 404. Behind OMNIRA_HUB_ACCESS_API_ENABLED.

export type AccessMode = 'none' | 'read' | 'reply'

export interface AccessPerson {
  user_id: string
  email: string
  name: string
}

export interface AccessInstance {
  tenant_id: string
  name: string
  tenant_status: string
  contract_status: string
  admins: AccessPerson[]
  direct_agents: number
  hub_agents: number
}

export interface AccessGrant {
  tenant_id: string
  mode: 'read' | 'reply'
  valid_until?: string
}

export interface AccessAgent extends AccessPerson {
  hub_role: 'hub_agent' | 'hub_admin'
  grants: AccessGrant[]
  direct_instances: string[]
  instances: number
}

export interface AccessOverview {
  hub_id: string
  hub_name: string
  instances: AccessInstance[]
  agents: AccessAgent[]
}

export const hubAccessAPI = {
  overview: (hubId: string): Promise<AccessOverview> =>
    axios.get<AccessOverview>(`${API_BASE}/hubs/${enc(hubId)}/access`, { headers: authHeaders() }).then((r) => r.data),
  addAgent: (hubId: string, email: string): Promise<AccessPerson> =>
    axios.post<AccessPerson>(`${API_BASE}/hubs/${enc(hubId)}/access/agents`, { email }, { headers: authHeaders() }).then((r) => r.data),
  removeAgent: (hubId: string, userId: string): Promise<void> =>
    axios.delete(`${API_BASE}/hubs/${enc(hubId)}/access/agents/${enc(userId)}`, { headers: authHeaders() }).then(() => undefined),
  setAccess: (hubId: string, userId: string, tenantId: string, mode: AccessMode, validUntil?: string | null): Promise<void> =>
    axios
      .put(`${API_BASE}/hubs/${enc(hubId)}/access/agents/${enc(userId)}/instances/${enc(tenantId)}`, { mode, valid_until: validUntil ?? null }, { headers: authHeaders() })
      .then(() => undefined),
  addAdmin: (hubId: string, tenantId: string, email: string): Promise<AccessPerson> =>
    axios.post<AccessPerson>(`${API_BASE}/hubs/${enc(hubId)}/access/instances/${enc(tenantId)}/admins`, { email }, { headers: authHeaders() }).then((r) => r.data),
  removeAdmin: (hubId: string, tenantId: string, userId: string): Promise<void> =>
    axios.delete(`${API_BASE}/hubs/${enc(hubId)}/access/instances/${enc(tenantId)}/admins/${enc(userId)}`, { headers: authHeaders() }).then(() => undefined),
}

export function describeHubAccessError(err: unknown): string {
  const e = err as { response?: { status?: number; data?: unknown } }
  const status = e?.response?.status
  const body = typeof e?.response?.data === 'string' ? e.response.data.replace(/^access: invalid request: /, '').replace(/^provisioning: invalid request: /, '').trim() : ''
  switch (status) {
    case 404:
      return 'Você não administra este Hub, ou a pessoa/empresa não pertence a ele.'
    case 400:
      return 'Pedido inválido. Recarregue a tela e tente de novo.'
    case 422:
      return body || 'Os dados informados não foram aceitos.'
    default:
      return 'Não foi possível concluir a ação.'
  }
}
