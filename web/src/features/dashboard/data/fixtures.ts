import type { DashboardData, PeriodId } from '../types'

// Demo data from docs/reference-kits/omnira-ui-design/01-FOUNDATION/DATA-AND-FIXTURES.md.
// No credentials or provider secrets here — see that document's security rule.
const base: DashboardData = {
  metrics: [
    { id: 'active_conversations', label: 'Conversas ativas', value: '42', trend: { direction: 'up', percent: 12 } },
    { id: 'new_contacts', label: 'Novos contatos', value: '18', trend: { direction: 'up', percent: 8 } },
    { id: 'open_tickets', label: 'Tickets abertos', value: '27', trend: { direction: 'down', percent: 5 } },
    { id: 'avg_first_response', label: 'Tempo médio de resposta', value: '2m 14s', trend: { direction: 'down', percent: 32 } },
  ],
  channels: [
    { channel: 'whatsapp', label: 'WhatsApp', total: 128, buckets: [12, 15, 18, 14, 20, 19, 17] },
    { channel: 'instagram', label: 'Instagram', total: 24, buckets: [3, 4, 2, 5, 3, 4, 3] },
    { channel: 'facebook', label: 'Facebook', total: 18, buckets: [2, 3, 3, 2, 4, 2, 2] },
    { channel: 'webchat', label: 'Web Chat', total: 12, buckets: [1, 2, 2, 1, 2, 2, 2] },
    { channel: 'email', label: 'E-mail', total: 6, buckets: [1, 1, 0, 1, 1, 1, 1] },
  ],
  ticketStatus: [
    { status: 'open', label: 'Abertos', count: 27 },
    { status: 'in_progress', label: 'Em atendimento', count: 34 },
    { status: 'waiting_customer', label: 'Aguardando cliente', count: 12 },
    { status: 'resolved', label: 'Resolvidos', count: 14 },
  ],
  recentConversations: [
    { id: 'c1', contact: 'Mariana Silva', preview: 'Olá! Gostaria de saber sobre meu pedido...', channel: 'whatsapp', time: '10:24', unreadCount: 2 },
    { id: 'c2', contact: 'Carlos Mendes', preview: 'Vocês têm esse produto em estoque?', channel: 'instagram', time: '10:20', unreadCount: 1 },
    { id: 'c3', contact: 'Ana Paula', preview: 'Perfeito, muito obrigado!', channel: 'whatsapp', time: '10:18', unreadCount: 0 },
    { id: 'c4', contact: 'João Ribeiro', preview: 'O prazo de entrega é para qual região?', channel: 'facebook', time: '10:15', unreadCount: 0 },
  ],
  priorityTickets: [
    { id: '#1042', subject: 'Problema com pagamento', contact: 'Mariana Silva', time: '10:12', priority: 'high', status: 'in_progress' },
    { id: '#1041', subject: 'Dúvida sobre garantia', contact: 'Rafael Costa', time: '09:48', priority: 'medium', status: 'open' },
    { id: '#1039', subject: 'Produto com defeito', contact: 'Juliana Alves', time: '09:30', priority: 'high', status: 'waiting_customer' },
    { id: '#1038', subject: 'Informações gerais', contact: 'Pedro Henrique', time: '09:21', priority: 'low', status: 'resolved' },
  ],
}

// Period only scales the demo volumes so the selector visibly does something.
const SCALE: Record<PeriodId, number> = {
  today: 0.35,
  last_24h: 1,
  last_7d: 4.2,
  last_30d: 15,
}

export function fixtureDashboard(period: PeriodId): DashboardData {
  const k = SCALE[period]
  if (k === 1) return base
  const scale = (n: number) => Math.max(0, Math.round(n * k))
  return {
    ...base,
    metrics: base.metrics.map((m) =>
      m.id === 'avg_first_response' ? m : { ...m, value: String(scale(Number(m.value))) },
    ),
    channels: base.channels.map((c) => ({ ...c, total: scale(c.total), buckets: c.buckets.map(scale) })),
    ticketStatus: base.ticketStatus.map((s) => ({ ...s, count: scale(s.count) })),
  }
}
