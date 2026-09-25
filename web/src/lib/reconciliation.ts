import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// PRODUCT.7A2: read-only client for the ticket reconciliation/audit
// surface (backend: internal/tickets/adapters/reconciliation_http.go,
// PRODUCT.7A1). Two independent resources, never merged — create and
// status attempts have materially different fields (target_status/
// confirmed_external_status only exist on status attempts;
// local_ticket_id/provider/external_ticket_id are nullable only on create
// attempts). No mutation method exists here by construction: this module
// never imports axios.post/patch/delete.

export type AttemptState = 'in_flight' | 'confirmed_success' | 'confirmed_failure' | 'outcome_unknown';

export interface ReconciliationActor {
  id: string;
  display_name: string;
  email: string;
}

// Mirrors CreateAttemptItem (reconciliation_http.go) exactly. Never a
// mutation endpoint — GET only.
export interface CreateAttemptItem {
  id: string;
  state: AttemptState;
  conversation_id: string;
  local_ticket_id: string | null;
  local_ticket_subject: string | null;
  provider: string | null;
  external_ticket_id: string | null;
  actor: ReconciliationActor;
  created_at: string;
  updated_at: string;
  projection_synced_at: string | null;
  idempotency_key_redacted: string;
}

// Mirrors StatusAttemptItem exactly. local_ticket_id/provider/
// external_ticket_id are always present here (a status mutation only ever
// targets an already-linked ticket) — kept non-nullable to match the
// backend DTO's own invariant, deliberately not unified with
// CreateAttemptItem's nullable shape.
export interface StatusAttemptItem {
  id: string;
  state: AttemptState;
  conversation_id: string;
  local_ticket_id: string;
  local_ticket_subject: string | null;
  provider: string;
  external_ticket_id: string;
  target_status: string;
  confirmed_external_status: string | null;
  confirmed_external_status_label: string | null;
  actor: ReconciliationActor;
  created_at: string;
  updated_at: string;
  projection_synced_at: string | null;
  idempotency_key_redacted: string;
}

export interface ReconciliationPage<T> {
  items: T[];
  has_more: boolean;
  next_cursor?: string;
  count: number;
  limit: number;
}

export interface ReconciliationFilters {
  state?: AttemptState;
  provider?: string;
  externalTicketId?: string;
  // ISO 8601 / RFC3339 timestamps — the backend parses with time.RFC3339.
  createdFrom?: string;
  createdTo?: string;
}

const reconciliationBase = () => `${API_BASE}/tenants/${getTenantId()}/ticket-reconciliation`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

function filterParams(filters?: ReconciliationFilters) {
  return {
    ...(filters?.state ? { state: filters.state } : {}),
    ...(filters?.provider ? { provider: filters.provider } : {}),
    ...(filters?.externalTicketId ? { external_ticket_id: filters.externalTicketId } : {}),
    ...(filters?.createdFrom ? { created_from: filters.createdFrom } : {}),
    ...(filters?.createdTo ? { created_to: filters.createdTo } : {}),
  };
}

export const reconciliationAPI = {
  listCreateAttempts: (cursor?: string, limit?: number, filters?: ReconciliationFilters) =>
    call<ReconciliationPage<CreateAttemptItem>>(() =>
      axios.get(`${reconciliationBase()}/create`, {
        headers: authHeaders(),
        params: {
          ...(cursor ? { cursor } : {}),
          ...(limit ? { limit } : {}),
          ...filterParams(filters),
        },
      }),
    ),
  listStatusAttempts: (cursor?: string, limit?: number, filters?: ReconciliationFilters) =>
    call<ReconciliationPage<StatusAttemptItem>>(() =>
      axios.get(`${reconciliationBase()}/status`, {
        headers: authHeaders(),
        params: {
          ...(cursor ? { cursor } : {}),
          ...(limit ? { limit } : {}),
          ...filterParams(filters),
        },
      }),
    ),
};

export function reconciliationErrorMessage(err: any, fallback = 'Não foi possível carregar os dados de reconciliação'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para visualizar a reconciliação de chamados deste tenant.';
    default:
      return fallback;
  }
}
