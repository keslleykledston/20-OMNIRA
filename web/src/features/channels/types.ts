import type { ChannelConnection } from '../../lib/integrations'

/**
 * Canonical UI state. Provider/session states from the backend never reach the
 * presentation layer directly — everything goes through `toConnectionState`.
 */
export type ConnectionState =
  | 'preparing'
  | 'qr_required'
  | 'connecting'
  | 'connected'
  | 'degraded'
  | 'disconnected'
  | 'failed'
  | 'disabled'

export function toConnectionState(c: ChannelConnection): ConnectionState {
  if (c.status === 'revoked') return 'disabled'
  if (c.status === 'failed' || c.session_status === 'failed') return 'failed'
  if (c.status === 'active') return 'connected'
  if (c.status === 'degraded') return 'degraded'

  switch (c.session_status) {
    case 'needs_qr':
      return 'qr_required'
    case 'starting':
      return 'preparing'
    case 'working':
      return 'connecting'
    case 'stopped':
      return 'disconnected'
    default:
      return 'disconnected'
  }
}

export const STATE_LABEL: Record<ConnectionState, string> = {
  preparing: 'Preparando',
  qr_required: 'Aguardando QR',
  connecting: 'Conectando',
  connected: 'Conectado',
  degraded: 'Instável',
  disconnected: 'Desconectado',
  failed: 'Falha',
  disabled: 'Desativado',
}

export const STATE_VARIANT: Record<ConnectionState, 'success' | 'warning' | 'danger' | 'info' | 'default'> = {
  preparing: 'info',
  qr_required: 'warning',
  connecting: 'info',
  connected: 'success',
  degraded: 'warning',
  disconnected: 'default',
  failed: 'danger',
  disabled: 'default',
}

export type ChannelId = 'whatsapp' | 'instagram' | 'facebook' | 'webchat' | 'email' | 'telegram'

export interface ChannelTab {
  id: ChannelId | 'all'
  label: string
}

export const CHANNEL_TABS: ChannelTab[] = [
  { id: 'all', label: 'Todos' },
  { id: 'whatsapp', label: 'WhatsApp' },
  { id: 'instagram', label: 'Instagram' },
  { id: 'facebook', label: 'Facebook' },
  { id: 'webchat', label: 'Web Chat' },
  { id: 'email', label: 'E-mail' },
  { id: 'telegram', label: 'Telegram' },
]

export const PROVIDER_KIND_LABEL = {
  official: 'Oficial',
  unofficial: 'Não Oficial',
} as const

/** Formats an external account id as a phone number when it looks like one. */
export function formatAccount(id?: string): string | null {
  if (!id) return null
  const digits = id.replace(/\D/g, '')
  if (digits.length < 10 || digits.length > 15) return id
  const cc = digits.slice(0, 2)
  const area = digits.slice(2, 4)
  const rest = digits.slice(4)
  const head = rest.slice(0, rest.length - 4)
  const tail = rest.slice(-4)
  return `+${cc} ${area} ${head}-${tail}`
}
