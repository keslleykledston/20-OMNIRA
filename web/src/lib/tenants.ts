import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, TENANT_KEY } from './session'
import { clearActing, setActing } from './acting'

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
  clearActing() // an instance of one's own is never entered as a hub agent
  try {
    localStorage.setItem(TENANT_KEY, id)
    localStorage.setItem(PREFERRED_KEY, id)
  } catch {
    // Storage blocked: the tenant cannot be switched; the page reloads unchanged.
  }
  go('/')
}

// An instance the person reaches only through the Hub, opened with its full workspace (ADR-0040): the session is pointed at that instance AND
// marked as acting for the hub, so every request for it declares the context. It is NOT remembered as a preferred own tenant. Full navigation, for the
// same reason as switchTenant.
export function enterDelegatedInstance(id: string, hubId: string, name: string, go: (path: string) => void = (p) => window.location.assign(p)): void {
  setActing(hubId, name)
  try {
    localStorage.setItem(TENANT_KEY, id)
  } catch {
    // Storage blocked: the context cannot be entered; the page reloads unchanged.
  }
  go(`/inbox?instancia=${encodeURIComponent(id)}`)
}

// Back to the person's own world: the delegated context is dropped and the session tenant becomes one of THEIR OWN (the one they last chose when it is
// still theirs, else the first), or none for someone who has no instance of their own. Without this a hub-only instance would stay as the session tenant.
export function leaveDelegatedInstance(own: MyTenant[]): void {
  clearActing()
  const preferred = readPreferred()
  const next = own.find((t) => t.id === preferred)?.id ?? own[0]?.id ?? ''
  try {
    if (next) localStorage.setItem(TENANT_KEY, next)
    else localStorage.removeItem(TENANT_KEY)
  } catch {
    // nothing more to do
  }
}

// Short code shown beside a company name so operators who serve several companies can tell them apart at a glance.
// It is NOT unique and never replaces the name: always render it together with the full name.
const CODE_STOPWORDS = new Set(['de', 'da', 'do', 'das', 'dos', 'e', 'ltda', 'me', 'eireli', 'epp', 'sa'])
export function tenantCode(name: string): string {
  const words = name
    .normalize('NFD')
    .replace(/[\u0300-\u036f]/g, '')
    .split(/[^A-Za-z0-9]+/)
    .filter((w) => w && !CODE_STOPWORDS.has(w.toLowerCase()))
  if (words.length === 0) return '?'
  if (words.length === 1) return words[0].slice(0, 3).toUpperCase()
  return words
    .slice(0, 3)
    .map((w) => w[0])
    .join('')
    .toUpperCase()
}
