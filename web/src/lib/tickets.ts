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
  // PRODUCT.6-D (ADR-0013): external ERP ticket projection/link, all null
  // for a local-only ticket — no real ticketing connector is wired for any
  // tenant yet (PRODUCT.6-B containment). provider is a free-form value
  // from the backend, never a hardcoded provider name on the client.
  provider: string | null;
  external_ticket_id: string | null;
  external_status: string | null;
  external_status_label: string | null;
  sync_status: string | null;
  last_synced_at: string | null;
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

export interface TicketExport {
  blob: Blob;
  filename: string;
}

// Extracts the filename from a real Content-Disposition header
// (attachment; filename="tickets.csv"); falls back to the canonical name
// if the header is absent or unparsable for any reason.
function filenameFromContentDisposition(header: string | undefined): string {
  const match = header?.match(/filename="?([^"]+)"?/);
  return match?.[1] ?? 'tickets.csv';
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
  // Exports the full filtered result (bounded server-side, PRODUCT.5-A) —
  // never just the currently visible pagination page.
  exportCSV: async (filters?: TicketFilters): Promise<TicketExport> => {
    try {
      const res = await axios.get(`${ticketsBase()}/export.csv`, {
        headers: authHeaders(),
        responseType: 'blob',
        params: {
          ...(filters?.status ? { status: filters.status } : {}),
          ...(filters?.priority ? { priority: filters.priority } : {}),
        },
      });
      return { blob: res.data, filename: filenameFromContentDisposition(res.headers['content-disposition']) };
    } catch (err) {
      if (isUnauthorized(err)) handleUnauthorized();
      throw err;
    }
  },
};

export function ticketErrorMessage(err: any, fallback = 'Não foi possível carregar os tickets'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para visualizar os tickets deste tenant.';
    case 413:
      return 'A exportação excede 5000 tickets. Refine os filtros e tente novamente.';
    default:
      return fallback;
  }
}
