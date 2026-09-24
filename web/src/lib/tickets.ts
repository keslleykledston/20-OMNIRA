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

// PRODUCT.6-N: provider-neutral external ticket creation (backend:
// internal/inbox/adapters.CreateTicket, PRODUCT.6-M/6-M4). Mirrors the
// HTTP contract exactly — no K3G field names (companyId/name/content)
// ever appear here.
export interface ExternalTicketCreateRequest {
  selected_customer_external_id: string;
  subject: string;
  description: string;
}

export interface ExternalTicketCreateResponse {
  local_ticket_id: string;
  external_ticket_id: string;
  provider: string;
  sync_status: string;
  replayed: boolean;
}

export interface ExternalTicketProblem {
  error: string;
  code: string;
  attempt_id?: string;
  attempt_state?: string;
  local_ticket_id?: string;
  provider?: string;
  external_ticket_id?: string;
  sync_status?: string;
  severe?: boolean;
  provider_error_code?: string;
}

// ExternalTicketCreateResult is a discriminated result, not a thrown
// error, for every outcome the backend defines a stable meaning for
// (PRODUCT.6-M sections 8-10) — a 409 TICKET_RECONCILIATION_REQUIRED is
// exactly as "successful" an HTTP round trip as a 201, it just carries a
// different safe next action. Only truly unexpected transport failures
// (no response at all) become 'network_error'.
export type ExternalTicketCreateResult =
  | { kind: 'created'; data: ExternalTicketCreateResponse }
  | { kind: 'replayed'; data: ExternalTicketCreateResponse }
  | { kind: 'reconciliation_required'; problem: ExternalTicketProblem }
  | { kind: 'already_linked'; problem: ExternalTicketProblem }
  | { kind: 'definitive_failure'; problem: ExternalTicketProblem }
  | { kind: 'unavailable' }
  | { kind: 'conflict' }
  | { kind: 'invalid'; message: string }
  | { kind: 'forbidden' }
  | { kind: 'not_found' }
  | { kind: 'network_error' };

export async function createExternalTicket(
  conversationId: string,
  idempotencyKey: string,
  req: ExternalTicketCreateRequest,
): Promise<ExternalTicketCreateResult> {
  try {
    const res = await axios.post(
      `${API_BASE}/tenants/${getTenantId()}/conversations/${conversationId}/ticket`,
      req,
      { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey, 'Content-Type': 'application/json' } },
    );
    return res.status === 201 ? { kind: 'created', data: res.data } : { kind: 'replayed', data: res.data };
  } catch (err: any) {
    if (isUnauthorized(err)) {
      handleUnauthorized();
      return { kind: 'forbidden' };
    }
    const status = err?.response?.status;
    const body = err?.response?.data;
    switch (status) {
      case 400:
        return { kind: 'invalid', message: typeof body === 'string' ? body : 'Dados inválidos.' };
      case 403:
        return { kind: 'forbidden' };
      case 404:
        return { kind: 'not_found' };
      case 409:
        if (body && typeof body === 'object' && body.code === 'TICKET_DEFINITIVE_FAILURE') {
          return { kind: 'definitive_failure', problem: body as ExternalTicketProblem };
        }
        if (body && typeof body === 'object' && body.code === 'TICKET_RECONCILIATION_REQUIRED') {
          return { kind: 'reconciliation_required', problem: body as ExternalTicketProblem };
        }
        if (body && typeof body === 'object' && body.code === 'TICKET_ALREADY_LINKED') {
          // PRODUCT.6-M5: the active local ticket was already linked
          // before this (NEW Idempotency-Key) create intent — never a
          // replay of THIS call, never reconciliation-required, never a
          // retryable error.
          return { kind: 'already_linked', problem: body as ExternalTicketProblem };
        }
        return { kind: 'invalid', message: typeof body === 'string' ? body : 'Conflito de estado da conversa.' };
      case 422:
        return { kind: 'conflict' };
      case 503:
        return { kind: 'unavailable' };
      default:
        // No HTTP status at all (network/timeout/CORS/abort): the request
        // may or may not have reached the backend — never assumed safe to
        // silently retry with a new key.
        return { kind: 'network_error' };
    }
  }
}

// PRODUCT.6-O1F: provider-neutral conversation ticket READ (backend:
// internal/inbox/adapters.GetCurrentTicket, PRODUCT.6-O1). Local
// projection only — this never triggers a K3G call. Kept as its own
// function/types, separate from createExternalTicket's request/response
// shapes, even though they share the same route: read and create are
// different HTTP methods with different contracts.
export interface ConversationTicketReadResponse {
  local_ticket_id: string;
  linked: boolean;
  provider?: string;
  external_ticket_id?: string;
  external_status?: string;
  external_status_label?: string;
  sync_status?: string;
  last_synced_at?: string;
}

// ConversationTicketReadResult mirrors createExternalTicket's discriminated-
// result convention: every backend-defined outcome (PRODUCT.6-O1 section 3)
// is a stable case, not a thrown error.
export type ConversationTicketReadResult =
  | { kind: 'ok'; data: ConversationTicketReadResponse }
  // 404: no active local ticket for this conversation. NOT the same as
  // linked=false — CreateExternalTicket requires an existing active local
  // ticket, so this must never be interpreted as "safe to offer CREATE".
  | { kind: 'not_found' }
  // 409: fail-closed inconsistent partial linkage (PRODUCT.6-O1 section 4).
  | { kind: 'inconsistent'; problem?: ExternalTicketProblem }
  | { kind: 'forbidden' }
  | { kind: 'unavailable' }
  | { kind: 'network_error' };

export async function readConversationTicket(conversationId: string): Promise<ConversationTicketReadResult> {
  try {
    const res = await axios.get(
      `${API_BASE}/tenants/${getTenantId()}/conversations/${conversationId}/ticket`,
      { headers: authHeaders() },
    );
    return { kind: 'ok', data: res.data };
  } catch (err: any) {
    if (isUnauthorized(err)) {
      handleUnauthorized();
      return { kind: 'forbidden' };
    }
    const status = err?.response?.status;
    const body = err?.response?.data;
    switch (status) {
      case 403:
        return { kind: 'forbidden' };
      case 404:
        return { kind: 'not_found' };
      case 409:
        return { kind: 'inconsistent', problem: body && typeof body === 'object' ? (body as ExternalTicketProblem) : undefined };
      case 503:
        return { kind: 'unavailable' };
      default:
        // No HTTP status at all: transport failure. Never assumed safe to
        // interpret as "no active ticket" or "not linked" — fail closed
        // with respect to offering CREATE.
        return { kind: 'network_error' };
    }
  }
}

// PRODUCT.6-O1RF: explicit provider projection refresh (backend:
// internal/inbox/adapters.RefreshTicket, PRODUCT.6-O1R). A COMMAND (POST),
// never a read — it performs exactly one provider GetTicket and updates
// only provider-owned freshness metadata. No body: refresh carries no
// CREATE-style intent (no company/subject/description, no
// Idempotency-Key — there is no write-duplication risk to guard against,
// since the provider is only ever read).
export type ConversationTicketRefreshResult =
  | { kind: 'ok'; data: ConversationTicketReadResponse }
  // 404: no active local ticket at all (should not normally be reachable —
  // refresh is only offered once a local ticket is known to be linked).
  | { kind: 'not_found' }
  // 409: covers every reconciliation-required refresh outcome the backend
  // defines (PRODUCT.6-O1R section 12) — ticket not linked, inconsistent
  // local linkage, a resolved-provider mismatch, an external id mismatch,
  // and provider NOT_FOUND/NOT_MIGRATED. The backend's HTTP contract does
  // not distinguish these with a machine-readable code (plain-text 409
  // body, same as failReadConversationTicket's existing convention), and
  // every one of them requires the exact same safe UI treatment: keep the
  // existing durable link visible, show "Verificação necessária", offer no
  // CREATE, make no further automatic request. A single case is therefore
  // the correct modeling, not a missing distinction.
  | { kind: 'reconciliation' }
  | { kind: 'forbidden' }
  | { kind: 'unavailable' }
  | { kind: 'network_error' };

export async function refreshConversationTicket(conversationId: string): Promise<ConversationTicketRefreshResult> {
  try {
    const res = await axios.post(
      `${API_BASE}/tenants/${getTenantId()}/conversations/${conversationId}/ticket/refresh`,
      undefined,
      { headers: authHeaders() },
    );
    return { kind: 'ok', data: res.data };
  } catch (err: any) {
    if (isUnauthorized(err)) {
      handleUnauthorized();
      return { kind: 'forbidden' };
    }
    const status = err?.response?.status;
    switch (status) {
      case 403:
        return { kind: 'forbidden' };
      case 404:
        return { kind: 'not_found' };
      case 409:
        return { kind: 'reconciliation' };
      case 503:
        return { kind: 'unavailable' };
      default:
        // No HTTP status at all: transport failure. The existing local
        // projection remains the last known evidence — never reinterpreted
        // as unlinked, never automatically retried.
        return { kind: 'network_error' };
    }
  }
}
