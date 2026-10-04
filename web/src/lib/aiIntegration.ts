import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

// Mirrors AIIntegration in internal/tenancy/adapters/ai_integration_http.go (ADR-0016).
// The API key is write-only: it is sent once and never comes back, only `key_configured`.
export interface AITest {
  at: string
  ok: boolean
  message: string
  model_available?: boolean
}

export interface AIIntegration {
  provider: 'gemini'
  enabled: boolean
  model: string
  monthly_budget_usd: number
  key_configured: boolean
  key_set_at?: string
  key_set_by?: string
  consent_text: string
  consent_version: string
  consent_current: boolean
  consent_at?: string
  consent_by?: string
  last_test?: AITest
}

export interface AIIntegrationUpdate {
  enabled?: boolean
  model?: string
  monthly_budget_usd?: number
  api_key?: string
  accept_external_ai?: boolean
}

const base = () => `${API_BASE}/tenants/${getTenantId()}/integrations/ai`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const aiIntegrationAPI = {
  get: () => call<AIIntegration>(() => axios.get(base(), { headers: authHeaders() })),
  update: (body: AIIntegrationUpdate) => call<AIIntegration>(() => axios.put(base(), body, { headers: authHeaders() })),
  removeKey: () => call<AIIntegration>(() => axios.delete(`${base()}/key`, { headers: authHeaders() })),
  test: () => call<AITest>(() => axios.post(base() + '/test', null, { headers: authHeaders() })),
}

// Same shape the API enforces for the key, so the form can say what is wrong before sending. Null = valid.
export function validateAPIKey(key: string): string | null {
  const k = key.trim()
  if (!/^[A-Za-z0-9_-]{20,200}$/.test(k)) return 'A chave tem formato inválido (letras, números, "-" e "_"; ao menos 20 caracteres).'
  return null
}

export function validateBudget(raw: string): string | null {
  if (!/^\d+([.,]\d{1,2})?$/.test(raw.trim())) return 'Informe um valor em dólares, por exemplo 10 ou 12,50.'
  const n = Number(raw.replace(',', '.'))
  if (n < 0 || n > 10000) return 'O orçamento deve estar entre 0 e 10000.'
  return null
}

export function aiErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Somente administradores podem alterar esta integração.'
  if (status === 422) return 'Valores inválidos. Confira a chave, o modelo, o orçamento e o aceite do termo.'
  if (status === 429) return 'Aguarde alguns segundos entre os testes.'
  if (status === 409) return 'Configure uma chave antes de testar.'
  return 'Não foi possível concluir. Tente novamente.'
}
