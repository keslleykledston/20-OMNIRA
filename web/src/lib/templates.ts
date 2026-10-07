import axios from 'axios'
import { useQuery } from '@tanstack/react-query'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'

/** An approved WhatsApp Cloud API message template of a line. */
export interface LineTemplate {
  id: string
  name: string
  language: string
  category: string
  status: string
  /** Body with {{1}}, {{2}} placeholders. */
  body: string
  variable_count: number
  sendable: boolean
  unsupported_reason?: string
}

async function guarded<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export function useLineTemplates(connectionId: string | undefined, enabled = true) {
  const tenantId = getTenantId()
  return useQuery({
    queryKey: ['line-templates', tenantId, connectionId],
    queryFn: async () => {
      const d = await guarded(() => axios.get(`${API_BASE}/tenants/${tenantId}/channels/lines/${connectionId}/templates`, { headers: authHeaders() }))
      return ((d as { items?: LineTemplate[] })?.items ?? []) as LineTemplate[]
    },
    enabled: !!tenantId && !!connectionId && enabled,
    retry: false,
  })
}

export function syncTemplates(connectionId: string): Promise<{ synced: number; sendable: number }> {
  const tenantId = getTenantId()
  return guarded(() => axios.post(`${API_BASE}/tenants/${tenantId}/channels/connections/${connectionId}/templates/sync`, undefined, { headers: authHeaders() }))
}

export function sendTemplate(conversationId: string, templateId: string, params: string[], idempotencyKey: string) {
  const tenantId = getTenantId()
  return guarded(() =>
    axios.post(
      `${API_BASE}/tenants/${tenantId}/inbox/conversations/${conversationId}/template`,
      { template_id: templateId, params },
      { headers: { ...authHeaders(), 'Idempotency-Key': idempotencyKey } },
    ),
  )
}

/** The body with the variables filled in (what the contact will read); unfilled ones stay as {{n}}. */
export function renderTemplate(body: string, params: string[]): string {
  return body.replace(/\{\{(\d{1,2})\}\}/g, (m, n: string) => {
    const v = params[Number(n) - 1]?.trim()
    return v ? v : m
  })
}

/** Same rules the server enforces, so the form can say what is wrong before sending. Null = valid. */
export function paramError(value: string): string | null {
  if (!value.trim()) return 'Preencha este campo.'
  if (/[\n\r\t]/.test(value) || value.includes('    ')) return 'Sem quebra de linha, tabulação ou muitos espaços seguidos.'
  if ([...value].length > 1024) return 'No máximo 1024 caracteres.'
  return null
}

export function templateErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number; data?: unknown } })?.response?.status
  const body = (err as { response?: { data?: unknown } })?.response?.data
  const text = typeof body === 'string' ? body : ''
  if (status === 409) {
    return text.includes('assigned') ? 'Assuma o atendimento antes de enviar.' : 'A conversa mudou ou foi finalizada. Atualize e tente de novo.'
  }
  if (status === 403) return 'Você não pode enviar nesta conversa.'
  if (status === 422) return text ? text.charAt(0).toUpperCase() + text.slice(1) : 'Template ou variáveis inválidos.'
  return 'Não foi possível enviar o template.'
}
