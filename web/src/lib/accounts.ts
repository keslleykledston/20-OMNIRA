import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors internal/accounts/adapters/http.go (ADR-0018). A customer account is the local, stable identity of an organization
// the tenant serves; a provider (CRM) company is only a LINK to it.
export type AccountType = 'customer' | 'partner' | 'internal' | 'other';
export type AccountStatus = 'active' | 'inactive' | 'archived';

export const ACCOUNT_TYPE_LABEL: Record<AccountType, string> = {
  customer: 'Cliente',
  partner: 'Parceiro',
  internal: 'Interna',
  other: 'Outra',
};

export const ACCOUNT_STATUS_LABEL: Record<AccountStatus, string> = {
  active: 'Ativa',
  inactive: 'Inativa',
  archived: 'Arquivada',
};

export interface AccountExternalLink {
  id: string;
  provider: string;
  connection_id: string;
  external_company_id: string;
  external_name_snapshot?: string;
  status: 'active' | 'inactive';
  source: string;
  verified_at?: string;
}

export interface Account {
  id: string;
  name: string;
  account_type: AccountType;
  status: AccountStatus;
  created_at: string;
  updated_at: string;
  external_links?: AccountExternalLink[];
}

export interface AccountTicket {
  id: string;
  conversation_id: string;
  subject: string;
  status: string;
  priority: string;
  provider: string | null;
  external_ticket_id: string | null;
  created_at: string;
  updated_at: string;
}

const base = () => `${API_BASE}/tenants/${getTenantId()}/accounts`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const accountsAPI = {
  list: (filters: { q?: string; status?: AccountStatus } = {}) =>
    call<{ items: Account[] }>(() =>
      axios.get(base(), { headers: authHeaders(), params: { ...(filters.q ? { q: filters.q } : {}), ...(filters.status ? { status: filters.status } : {}) } }),
    ).then((d) => d.items ?? []),
  get: (id: string) => call<Account>(() => axios.get(`${base()}/${id}`, { headers: authHeaders() })),
  create: (body: { name: string; account_type: AccountType }) => call<Account>(() => axios.post(base(), body, { headers: authHeaders() })),
  update: (id: string, body: { name?: string; account_type?: AccountType; status?: AccountStatus }) =>
    call<Account>(() => axios.patch(`${base()}/${id}`, body, { headers: authHeaders() })),
  tickets: (id: string) => call<{ items: AccountTicket[] }>(() => axios.get(`${base()}/${id}/tickets`, { headers: authHeaders() })).then((d) => d.items ?? []),
};

export function accountErrorMessage(err: any, fallback = 'Não foi possível concluir. Tente novamente.'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para esta ação.';
    case 404:
      return 'Empresa não encontrada.';
    case 422:
      return 'Dados inválidos: confira o nome.';
    default:
      return fallback;
  }
}
