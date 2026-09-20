import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors ContactItem in internal/contacts/adapters/http.go. The API
// deliberately omits tenant_id: the session already establishes the tenant.
export interface Contact {
  id: string;
  display_name: string;
  phone_e164: string;
  email: string;
  status: 'active' | 'blocked' | 'archived';
  created_at: string;
  updated_at: string;
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
  list: (cursor?: string, limit?: number) =>
    call<ContactPage>(() =>
      axios.get(contactsBase(), {
        headers: authHeaders(),
        params: { ...(cursor ? { cursor } : {}), ...(limit ? { limit } : {}) },
      }),
    ),
  get: (id: string) =>
    call<Contact>(() => axios.get(`${contactsBase()}/${id}`, { headers: authHeaders() })),
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
