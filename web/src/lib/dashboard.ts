import axios from 'axios';
import { API_BASE } from './config';
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session';

// Mirrors internal/dashboard/adapters/http.go's Snapshot — only durable
// Postgres aggregates with an unambiguous canonical definition (PRODUCT.3-B
// V1). agents_online is realtime and comes from the existing presence
// snapshot (lib/presence.ts), never from this endpoint.
export interface DashboardSnapshot {
  open_conversations: number;
  open_tickets: number;
  total_contacts: number;
  // External contacts nobody has classified yet (ADR-0018). Conversations with staff never count as open conversations.
  unclassified_contacts?: number;
}

const dashboardBase = () => `${API_BASE}/tenants/${getTenantId()}/dashboard`;

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data;
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized();
    throw err;
  }
}

export const dashboardAPI = {
  snapshot: () => call<DashboardSnapshot>(() => axios.get(`${dashboardBase()}/snapshot`, { headers: authHeaders() })),
};

export function dashboardErrorMessage(err: any, fallback = 'Não foi possível carregar os indicadores'): string {
  switch (err?.response?.status) {
    case 403:
      return 'Você não tem permissão para visualizar o Dashboard deste tenant.';
    default:
      return fallback;
  }
}
