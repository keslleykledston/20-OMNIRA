import axios from 'axios'
import { API_BASE } from './config'
import { authHeaders, getTenantId, handleUnauthorized, isUnauthorized } from './session'
import { DEFAULT_WAIT_THRESHOLDS, type WaitThresholds } from './inboxModel'

// Mirrors InboxSettings in internal/tenancy/adapters/inbox_settings_http.go.
// Display-only thresholds (minutes) for the "customer is waiting" chip in the list.
export interface InboxSettings {
  wait_warn_minutes: number
  wait_danger_minutes: number
}

export const MAX_WAIT_DANGER_MINUTES = 10080 // one week; the API and the table CHECK enforce the same

export const DEFAULT_INBOX_SETTINGS: InboxSettings = {
  wait_warn_minutes: DEFAULT_WAIT_THRESHOLDS.warnMinutes,
  wait_danger_minutes: DEFAULT_WAIT_THRESHOLDS.dangerMinutes,
}

export function toThresholds(s: InboxSettings): WaitThresholds {
  return { warnMinutes: s.wait_warn_minutes, dangerMinutes: s.wait_danger_minutes }
}

const url = () => `${API_BASE}/tenants/${getTenantId()}/settings/inbox`

async function call<T>(fn: () => Promise<{ data: T }>): Promise<T> {
  try {
    return (await fn()).data
  } catch (err) {
    if (isUnauthorized(err)) handleUnauthorized()
    throw err
  }
}

export const inboxSettingsAPI = {
  get: () => call<InboxSettings>(() => axios.get(url(), { headers: authHeaders() })),
  update: (body: InboxSettings) => call<InboxSettings>(() => axios.put(url(), body, { headers: authHeaders() })),
}

/** Same rules the API enforces, so the form can say what is wrong before sending. Null = valid. */
export function validateWaitThresholds(warn: number, danger: number): string | null {
  if (!Number.isInteger(warn) || !Number.isInteger(danger) || warn < 1) {
    return 'Informe números inteiros de minutos (mínimo 1).'
  }
  if (danger <= warn) return 'O limite crítico precisa ser maior que o de atenção.'
  if (danger > MAX_WAIT_DANGER_MINUTES) return 'O limite crítico pode ser de no máximo 10080 minutos (7 dias).'
  return null
}

export function inboxSettingsErrorMessage(err: unknown): string {
  const status = (err as { response?: { status?: number } })?.response?.status
  if (status === 403) return 'Somente administradores podem alterar estes limites.'
  if (status === 422 || status === 400) return 'Valores inválidos: o crítico deve ser maior que a atenção (máximo 10080 minutos).'
  return 'Não foi possível salvar. Tente novamente.'
}
