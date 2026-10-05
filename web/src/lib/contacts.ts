import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors ContactItem in internal/contacts/adapters/http.go. The API
// deliberately omits tenant_id: the session already establishes the tenant.
// Who the contact is for the business (ADR-0014); distinct from `status`, the record lifecycle.
// ADR-0018: K3G staff are Users, never contacts — there is no "agent" kind. A new contact is "unclassified".
export type ContactKind = 'unclassified' | 'customer' | 'other' | 'spam';

export const CONTACT_KIND_LABEL: Record<ContactKind, string> = {
  unclassified: 'Não classificado',
  customer: 'Cliente',
  other: 'Outros',
  spam: 'Spam',
};

export type RelationshipType =
  | 'employee'
  | 'owner'
  | 'technical_contact'
  | 'billing_contact'
  | 'administrative_contact'
  | 'representative'
  | 'contractor'
  | 'other';

export const RELATIONSHIP_LABEL: Record<RelationshipType, string> = {
  employee: 'Funcionário',
  owner: 'Proprietário',
  technical_contact: 'Contato técnico',
  billing_contact: 'Financeiro',
  administrative_contact: 'Administrativo',
  representative: 'Representante',
  contractor: 'Prestador',
  other: 'Outro vínculo',
};

// Mirrors linkDTO / classificationView in internal/contacts/adapters/classification_http.go.
export interface ContactAccountLink {
  id: string;
  account_id: string;
  account_name: string;
  relationship_type: RelationshipType;
  status: 'active' | 'ended';
  primary: boolean;
  source: string;
  created_at: string;
  ended_at?: string;
}

export interface ContactClassification {
  kind: ContactKind;
  classification_source: string | null;
  classified_at: string | null;
  accounts: ContactAccountLink[];
}

// One company the contact belongs to: a company of the tenant's directory (revalidated by the server, which never
// trusts the name/CNPJ shown here) OR an account that already exists locally. Exactly one of the two ids.
export interface AccountRef {
  account_id?: string;
  directory_company_id?: string;
  // Set when the company came from a suggestion: the server re-checks it belongs to THIS contact and company.
  evidence_id?: string;
  relationship_type?: RelationshipType;
  primary?: boolean;
}

// A company that integration evidence (a validated ticket selection) associated with the contact. Only a SUGGESTION:
// it never classifies or links anything until a person accepts it.
export interface CompanySuggestion {
  evidence_id: string;
  external_company_id: string;
  connection_id: string;
  source: string;
  first_verified_at: string;
  last_verified_at: string;
  account_id?: string;
  account_name?: string;
  already_linked: boolean;
}

export interface DirectoryCompany {
  id: string;
  name: string;
  cnpj?: string;
}

export interface CustomerAccount {
  id: string;
  name: string;
  account_type: string;
  status: string;
}

export interface ContactFilters {
  q?: string;
  status?: 'active' | 'blocked' | 'archived';
  kind?: ContactKind;
}

export interface Contact {
  id: string;
  display_name: string;
  phone_e164: string;
  email: string;
  status: 'active' | 'blocked' | 'archived';
  kind: ContactKind;
  created_at: string;
  updated_at: string;
  // Derived read-only facts (CONTACT.360-A), always present in API responses.
  last_interaction_at: string | null;
  channels: string[];
  open_conversation_count: number;
}

export interface ContactLastMessage {
  direction: 'inbound' | 'outbound';
  message_type: 'text' | 'image' | 'video' | 'audio' | 'document' | 'sticker';
  body_preview: string;
  created_at: string;
}

// Mirrors ContactConversationItem in internal/contacts/adapters/contact360.go.
export interface ContactConversation {
  id: string;
  status: string;
  title: string;
  channel: string | null;
  provider: string | null;
  assigned_to_user_id: string | null;
  message_count: number;
  last_message: ContactLastMessage | null;
  created_at: string;
  updated_at: string;
}

// Mirrors ContactTicketItem. Requires ticket.read: the API answers 403 without it.
export interface ContactTicket {
  id: string;
  conversation_id: string;
  subject: string;
  status: 'open' | 'in_progress' | 'waiting' | 'resolved' | 'closed';
  priority: 'critical' | 'high' | 'medium' | 'low';
  assigned_to: string | null;
  provider: string | null;
  external_ticket_id: string | null;
  external_status_label: string | null;
  // The OMNIRA account the ticket targets (ADR-0018); absent for tickets that predate accounts.
  customer_account_id?: string;
  customer_account_name?: string;
  created_at: string;
  updated_at: string;
}

export interface Page<T> {
  items: T[];
  has_more: boolean;
  next_cursor?: string;
  count: number;
  limit: number;
}

export interface ContactPage {
  items: Contact[];
  has_more: boolean;
  next_cursor?: string;
  count: number;
  limit: number;
}

const contactsBase = () => `${API_BASE}/tenants/${getTenantId()}/contacts`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const contactsAPI = {
  list: (cursor?: string, limit?: number, filters: ContactFilters = {}) =>
    call<ContactPage>(() =>
      axios.get(contactsBase(), {
        headers: authHeaders(),
        params: {
          ...(cursor ? { cursor } : {}),
          ...(limit ? { limit } : {}),
          ...(filters.q ? { q: filters.q } : {}),
          ...(filters.status ? { status: filters.status } : {}),
          ...(filters.kind ? { kind: filters.kind } : {}),
        },
      }),
    ),
  // Any attending role may classify (conversation.claim); spam is never deleted, it has its own Inbox.
  setKind: (id: string, kind: ContactKind) =>
    call<Contact>(() => axios.patch(`${contactsBase()}/${id}`, { kind }, { headers: authHeaders() })),
  get: (id: string) =>
    call<Contact>(() => axios.get(`${contactsBase()}/${id}`, { headers: authHeaders() })),
  conversations: (id: string, cursor?: string, limit?: number) =>
    call<Page<ContactConversation>>(() =>
      axios.get(`${contactsBase()}/${id}/conversations`, {
        headers: authHeaders(),
        params: { ...(cursor ? { cursor } : {}), ...(limit ? { limit } : {}) },
      }),
    ),
  tickets: (id: string, cursor?: string, limit?: number) =>
    call<Page<ContactTicket>>(() =>
      axios.get(`${contactsBase()}/${id}/tickets`, {
        headers: authHeaders(),
        params: { ...(cursor ? { cursor } : {}), ...(limit ? { limit } : {}) },
      }),
    ),
};

export const classificationAPI = {
  suggestions: (id: string) =>
    call<{ items: CompanySuggestion[] }>(() => axios.get(`${contactsBase()}/${id}/company-suggestions`, { headers: authHeaders() })),
  get: (id: string) =>
    call<ContactClassification>(() => axios.get(`${contactsBase()}/${id}/classification`, { headers: authHeaders() })),
  // Kind and companies change in ONE transaction on the server. `customer` needs at least one company.
  put: (id: string, body: { kind: ContactKind; accounts?: AccountRef[]; end_links?: boolean }) =>
    call<ContactClassification>(() => axios.put(`${contactsBase()}/${id}/classification`, body, { headers: authHeaders() })),
  linkAccount: (id: string, ref: AccountRef) =>
    call<ContactAccountLink>(() => axios.post(`${contactsBase()}/${id}/accounts`, ref, { headers: authHeaders() })),
  // Soft end. The last company of a customer needs `reclassify_to` (other | unclassified).
  endLink: (id: string, linkId: string, reclassifyTo?: 'other' | 'unclassified') =>
    call<ContactClassification>(() =>
      axios.post(`${contactsBase()}/${id}/accounts/${linkId}/end`, reclassifyTo ? { reclassify_to: reclassifyTo } : {}, { headers: authHeaders() }),
    ),
  setPrimary: (id: string, linkId: string) =>
    call<ContactClassification>(() => axios.post(`${contactsBase()}/${id}/accounts/${linkId}/primary`, {}, { headers: authHeaders() })),
};

export const accountDirectoryAPI = {
  // The tenant's provider company directory (active companies only).
  directory: () =>
    call<{ items: DirectoryCompany[] }>(() => axios.get(`${API_BASE}/tenants/${getTenantId()}/crm/companies`, { headers: authHeaders() })),
  localAccounts: (q?: string) =>
    call<{ items: CustomerAccount[] }>(() =>
      axios.get(`${API_BASE}/tenants/${getTenantId()}/accounts`, { headers: authHeaders(), params: { status: 'active', ...(q ? { q } : {}) } }),
    ),
};

export function contactErrorMessage(err: any, fallback = 'Não foi possível carregar os contatos'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem acesso aos contatos deste tenant.';
    case 404:
      // A contact of another tenant is indistinguishable from one that does not
      // exist — both answer 404, by design.
      return 'Contato não encontrado.';
    default:
      return fallback;
  }
}

export function contactKindErrorMessage(err: any): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para classificar contatos.';
    case 404:
      return 'Contato não encontrado.';
    case 409:
      return 'Esta é a última empresa do cliente. Reclassifique o contato junto com a remoção.';
    case 422:
      return 'Um cliente precisa de pelo menos uma empresa vinculada (ativa e válida).';
    case 503:
      return 'O diretório de empresas não está disponível agora.';
    default:
      return 'Não foi possível salvar. Tente novamente.';
  }
}
