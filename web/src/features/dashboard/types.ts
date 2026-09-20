export type TrendDirection = 'up' | 'down'

export interface Metric {
  id: 'active_conversations' | 'new_contacts' | 'open_tickets' | 'avg_first_response'
  label: string
  value: string
  trend: { direction: TrendDirection; percent: number }
}

export type ChannelId = 'whatsapp' | 'instagram' | 'facebook' | 'webchat' | 'email'

export interface ChannelSeries {
  channel: ChannelId
  label: string
  total: number
  /** one bucket per period step, used by the bar chart */
  buckets: number[]
}

export type TicketStatusId = 'open' | 'in_progress' | 'waiting_customer' | 'resolved'

export interface TicketStatusSlice {
  status: TicketStatusId
  label: string
  count: number
}

export interface RecentConversation {
  id: string
  contact: string
  preview: string
  channel: ChannelId
  time: string
  unreadCount: number
}

export type TicketPriority = 'high' | 'medium' | 'low'

export interface PriorityTicket {
  id: string
  subject: string
  contact: string
  time: string
  priority: TicketPriority
  status: TicketStatusId
}

export interface DashboardData {
  metrics: Metric[]
  channels: ChannelSeries[]
  ticketStatus: TicketStatusSlice[]
  recentConversations: RecentConversation[]
  priorityTickets: PriorityTicket[]
}

export type PeriodId = 'today' | 'last_24h' | 'last_7d' | 'last_30d'

export interface Period {
  id: PeriodId
  label: string
}

export const PERIODS: Period[] = [
  { id: 'today', label: 'Hoje' },
  { id: 'last_24h', label: 'Últimas 24 horas' },
  { id: 'last_7d', label: 'Últimos 7 dias' },
  { id: 'last_30d', label: 'Últimos 30 dias' },
]
