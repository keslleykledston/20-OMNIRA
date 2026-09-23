import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors TicketItem in internal/tickets/adapters/http.go. tenant_id is
// omitted — the session already establishes it. Fields match the canonical
// Ticket (internal/tickets/domain) exactly; no SLA/account/channel fields
// exist yet.
export interface Ticket {
  id: string;
  conversation_id: string;
  subject: string;
  status: 'open' | 'in_progress' | 'waiting' | 'resolved' | 'closed';
  priority: 'critical' | 'high' | 'medium' | 'low';
  assigned_to: string | null;
  created_at: string;
  updated_at: string;
}

export interface TicketPage {
  items: Ticket[];
  has_more: boolean;
  next_cursor?: string;
  count: number;
  limit: number;
}

export interface TicketFilters {
  status?: Ticket['status'];
  priority?: Ticket['priority'];
}

const ticketsBase = () => `${API_BASE}/tenants/${getTenantId()}/tickets`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const ticketsAPI = {
  list: (cursor?: string, limit?: number, filters?: TicketFilters) =>
    call<TicketPage>(() =>
      axios.get(ticketsBase(), {
        headers: authHeaders(),
        params: {
          ...(cursor ? { cursor } : {}),
          ...(limit ? { limit } : {}),
          ...(filters?.status ? { status: filters.status } : {}),
          ...(filters?.priority ? { priority: filters.priority } : {}),
        },
      }),
    ),
};

export function ticketErrorMessage(err: any, fallback = 'Não foi possível carregar os tickets'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para visualizar os tickets deste tenant.';
    default:
      return fallback;
  }
}
