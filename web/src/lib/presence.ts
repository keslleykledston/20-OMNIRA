import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// IAM4.2-A (ADR-0010): presence is boolean online/offline, per session/tab,
// aggregated per AgentProfile. This module never sends tenant_id,
// membership_id, agent_profile_id or user_id — those are resolved
// server-side from the authenticated session; the client only ever supplies
// its own ephemeral session_id.

const SESSION_STORAGE_KEY = 'omnira.presence.session_id'

/** One id per browser tab, generated once and kept for the tab's lifetime
 * (sessionStorage, not localStorage: a new tab must be a new session). */
export function getPresenceSessionId(): string {
  let id = sessionStorage.getItem(SESSION_STORAGE_KEY)
  if (!id) {
    id = crypto.randomUUID()
    sessionStorage.setItem(SESSION_STORAGE_KEY, id)
  }
  return id
}

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (error) {
    if (isUnauthorized(error)) handleUnauthorized()
    throw error
  }
}

export interface PresenceSnapshot {
  online_agent_profile_ids: string[]
}

export interface PresenceTransitionEvent {
  tenant_id: string
  agent_profile_id: string
  status: 'online' | 'offline'
  occurred_at: string
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`

export const presenceAPI = {
  /** Fire-and-forget from the caller's point of view: a 403 means the
   * current user has no active AgentProfile (e.g. a tenant_admin who is not
   * an operational agent) — expected, not an error to surface. */
  heartbeat: () =>
    axios
      .post(`${base()}/me/presence/heartbeat`, { session_id: getPresenceSessionId() }, { headers: authHeaders() })
      .then(() => true)
      .catch((error) => {
        if (isUnauthorized(error)) {
          handleUnauthorized()
          return false
        }
        if (axios.isAxiosError(error) && error.response?.status === 403) return false
        throw error
      }),
  snapshot: () => call<PresenceSnapshot>(() => axios.get(`${base()}/agents/presence`, { headers: authHeaders() })),
}
