import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, TENANT_KEY } from './session'

// Mirrors MyTenant in contracts/openapi/omnira-v1.yaml (GET /tenants): the
// tenants the signed-in user has an active membership in, already ordered.
export interface MyTenant {
  id: string
  legal_name: string
  trade_name?: string
}

// Kept across logouts on purpose (clearSession does not touch it): it only
// expresses which of the user's OWN tenants they last chose, and it is honoured
// only if that tenant is in the list the API returns for the new session.
const PREFERRED_KEY = 'preferredTenantId'

export const tenantDisplayName = (t: MyTenant): string => t.trade_name?.trim() || t.legal_name

export const tenantsAPI = {
  mine: () => axios.get<MyTenant[]>(`${API_BASE}/tenants`, { headers: authHeaders() }).then((r) => r.data),
}

function readPreferred(): string {
  try {
    return localStorage.getItem(PREFERRED_KEY) || ''
  } catch {
    return ''
  }
}

// After login the server proposes a default tenant (the user's oldest
// membership). A user who belongs to several tenants and chose one before gets
// that one back, but only when it is really one of theirs: the id comes from
// the browser, so it is never trusted by itself.
export async function resolveSessionTenant(serverDefault: string | undefined): Promise<string | undefined> {
  const preferred = readPreferred()
  if (!preferred || preferred === serverDefault) return serverDefault
  try {
    const mine = await tenantsAPI.mine()
    return mine.some((t) => t.id === preferred) ? preferred : serverDefault
  } catch {
    return serverDefault
  }
}

// A full navigation (not a client-side route change): caches, open realtime
// streams and per-tenant state all start clean in the new tenant.
export function switchTenant(id: string, go: (path: string) => void = (p) => window.location.assign(p)): void {
  try {
    localStorage.setItem(TENANT_KEY, id)
    localStorage.setItem(PREFERRED_KEY, id)
  } catch {
    // Storage blocked: the tenant cannot be switched; the page reloads unchanged.
  }
  go('/')
}
