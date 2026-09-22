import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

export type AgentStatus = 'active' | 'disabled'
export interface QueueAssignment { id: string; queue_id: string; queue_name: string; available: boolean; capacity: number }
export interface OperationalAgent { id: string; membership_id: string; user_id: string; name: string; email: string; role: string; status: AgentStatus; queues: QueueAssignment[] }

const base = () => `${API_BASE}/tenants/${getTenantId()}/agents`
async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try { return (await fn()).data } catch (error) { if (isUnauthorized(error)) handleUnauthorized(); throw error }
}
export const agentsAPI = {
  list: () => call<{ items: OperationalAgent[] }>(() => axios.get(base(), { headers: authHeaders() })).then((r) => r.items),
  setStatus: (id: string, status: AgentStatus) => call<void>(() => axios.patch(`${base()}/${id}`, { status }, { headers: authHeaders() })),
  addQueue: (id: string, queueID: string, available: boolean, capacity: number) => call<{ id: string }>(() => axios.post(`${base()}/${id}/queues`, { queue_id: queueID, available, capacity }, { headers: authHeaders() })),
  updateQueue: (id: string, memberID: string, available: boolean, capacity: number) => call<void>(() => axios.patch(`${base()}/${id}/queues/${memberID}`, { available, capacity }, { headers: authHeaders() })),
  removeQueue: (id: string, memberID: string) => call<void>(() => axios.delete(`${base()}/${id}/queues/${memberID}`, { headers: authHeaders() })),
}
