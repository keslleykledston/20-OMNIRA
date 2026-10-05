import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors ContactItem in internal/contacts/adapters/http.go. The API
// deliberately omits tenant_id: the session already establishes the tenant.
// Who the contact is for the business (ADR-0014); distinct from `status`, the record lifecycle.
export type ContactKind = 'customer' | 'other' | 'agent' | 'spam';

export const CONTACT_KIND_LABEL: Record<ContactKind, string> = {
  customer: 'Cliente',
  other: 'Outros',
  agent: 'Agente',
  spam: 'Spam',
};

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
    default:
      return 'Não foi possível salvar. Tente novamente.';
  }
}
