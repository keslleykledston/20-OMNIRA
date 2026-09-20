import type { ChannelId, TicketStatusId, TicketPriority } from '../types'

/** CSS var references so series colours stay tokenized. */
export const CHANNEL_COLOR: Record<ChannelId, string> = {
  whatsapp: 'var(--color-channel-whatsapp)',
  instagram: 'var(--color-channel-instagram)',
  facebook: 'var(--color-channel-facebook)',
  webchat: 'var(--color-channel-webchat)',
  email: 'var(--color-channel-email)',
}

// Adjacent donut slices must stay distinguishable: status-info and
// accent-primary are both blue, so resolved takes the categorical violet.
export const TICKET_STATUS_COLOR: Record<TicketStatusId, string> = {
  open: 'var(--color-status-info)',
  in_progress: 'var(--color-status-success)',
  waiting_customer: 'var(--color-status-warning)',
  resolved: 'var(--color-chart-violet)',
}

export const TICKET_STATUS_LABEL: Record<TicketStatusId, string> = {
  open: 'Abertos',
  in_progress: 'Em atendimento',
  waiting_customer: 'Aguardando cliente',
  resolved: 'Resolvido',
}

export const TICKET_STATUS_VARIANT: Record<TicketStatusId, 'info' | 'default' | 'warning' | 'success'> = {
  open: 'info',
  in_progress: 'default',
  waiting_customer: 'warning',
  resolved: 'success',
}

export const PRIORITY_LABEL: Record<TicketPriority, string> = {
  high: 'Alta',
  medium: 'Média',
  low: 'Baixa',
}

export const PRIORITY_VARIANT: Record<TicketPriority, 'danger' | 'warning' | 'success'> = {
  high: 'danger',
  medium: 'warning',
  low: 'success',
}
