import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

export type QueueMode = 'manual' | 'round_robin'

// Mirrors QueueItem in internal/tenancy/adapters/queues_http.go. The UI calls
// queues "grupos". member_count / available_count are queue ELIGIBILITY, not
// presence (who is online lives in /agents/presence).
export interface Queue {
  id: string
  name: string
  mode: QueueMode
  is_default: boolean
  member_count: number
  available_count: number
  open_conversation_count: number
  created_at: string
  updated_at: string
}

export interface QueuePatch {
  name?: string
  mode?: QueueMode
  is_default?: boolean
}

const base = () => `${API_BASE}/tenants/${getTenantId()}/queues`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const queuesAPI = {
  list: () =>
    call<{ items: Queue[] }>(() => axios.get(base(), { headers: authHeaders() })).then((r) => r.items),
  create: (body: { name: string; mode: QueueMode; is_default: boolean }) =>
    call<Queue>(() => axios.post(base(), body, { headers: authHeaders() })),
  update: (id: string, patch: QueuePatch) =>
    call<Queue>(() => axios.patch(`${base()}/${id}`, patch, { headers: authHeaders() })),
  remove: (id: string) => call<void>(() => axios.delete(`${base()}/${id}`, { headers: authHeaders() })),
}

// The API answers with short plain-text reasons; the screen shows a sentence the
// operator can act on instead of the raw text.
export function queueErrorMessage(err: any, fallback = 'Não foi possível concluir a alteração.'): string {
  const status = err?.response?.status
  const text = typeof err?.response?.data === 'string' ? err.response.data.toLowerCase() : ''
  if (status === 403) return 'Você não tem permissão para alterar grupos.'
  if (status === 404) return 'Este grupo não existe mais. Atualize a página.'
  if (status === 400) return 'Confira o nome: use de 1 a 60 caracteres.'
  if (status === 409) {
    if (text.includes('already exists')) return 'Já existe um grupo com esse nome.'
    if (text.includes('unsetting') || text.includes('choose another queue')) {
      return 'Para trocar o grupo padrão, escolha outro grupo como padrão.'
    }
    if (text.includes('default queue cannot be deleted')) {
      return 'O grupo padrão não pode ser excluído. Defina outro como padrão antes.'
    }
    if (text.includes('still has conversations')) return 'Este grupo ainda tem conversas e não pode ser excluído.'
  }
  return fallback
}
