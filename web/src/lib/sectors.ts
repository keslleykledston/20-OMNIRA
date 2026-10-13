import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors sectorItem in internal/sectors/adapters/handler.go (ADR-0044). The UI calls queues "grupos" and sectors "setores".
export interface Sector {
  id: string
  name: string
  active: boolean
  calendar_id?: string
  queues: { id: string; name: string }[]
}

const base = () => `${API_BASE}/tenants/${getTenantId()}`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const sectorsAPI = {
  list: () => call<{ items: Sector[] }>(() => axios.get(`${base()}/sectors`, { headers: authHeaders() })).then((r) => r.items),
}
