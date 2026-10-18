import type { MessageItem } from '../types/api'

// Events of the attendance (GET .../inbox/conversations/{id}/timeline): what happened TO it, drawn as discreet lines between the messages.
// Mirrors TimelineEvent in internal/inbox/adapters/timeline.go.
export type TimelineEventKind =
  | 'claimed'
  | 'assigned'
  | 'released'
  | 'transferred'
  | 'sent_to_queue'
  | 'taken_over'
  | 'auto_assigned'
  | 'external_claimed'
  | 'external_released'
  | 'protocol_opened'
  | 'protocol_closed'
  | 'finalized'

const KNOWN_KINDS: ReadonlySet<string> = new Set<TimelineEventKind>([
  'claimed', 'assigned', 'released', 'transferred', 'sent_to_queue', 'taken_over', 'auto_assigned',
  'external_claimed', 'external_released', 'protocol_opened', 'protocol_closed', 'finalized',
])

export interface TimelineEvent {
  id: string
  kind: TimelineEventKind
  at: string
  actor?: string
  actor_id?: string
  target?: string
  target_id?: string
  protocol?: string
  detail?: string
  system?: boolean
}

const FINALIZE_REASON: Record<string, string> = {
  resolved: 'Resolvido',
  no_response: 'Sem resposta do cliente',
  duplicate: 'Duplicado',
  spam: 'Spam',
  transferred: 'Transferido',
  other: 'Outro motivo',
}

/**
 * The line for an event, in the words of the team. A person is named when the server could name them; the reader is "Você"; someone the reader
 * may not look up (an agent reading a colleague) is "Um atendente" - the server tells the id, never more than the reader may see.
 */
export function timelineText(ev: TimelineEvent, selfId: string | null): string {
  const person = (name?: string, id?: string): string => (id && selfId && id === selfId ? 'Você' : name || (id ? 'Um atendente' : ''))
  const actor = person(ev.actor, ev.actor_id)
  const target = person(ev.target, ev.target_id)
  switch (ev.kind) {
    case 'claimed':
      return `${actor || 'Um atendente'} assumiu o atendimento`
    case 'assigned':
      return `${actor || 'Um atendente'} atribuiu o atendimento a ${target || 'um atendente'}`
    case 'released':
      return actor ? `${actor} soltou o atendimento` : 'Atendimento devolvido à fila'
    case 'transferred':
      return `${actor || 'Um atendente'} transferiu o atendimento para ${target || 'um atendente'}`
    case 'sent_to_queue':
      return `${actor || 'Um atendente'} enviou o atendimento para uma fila`
    case 'taken_over':
      return `${actor || 'Um supervisor'} retomou o atendimento do aplicativo externo`
    case 'auto_assigned':
      return ev.system ? `Distribuído automaticamente para ${target || 'um atendente'}` : `Atribuído a ${target || 'um atendente'}`
    case 'external_claimed':
      return 'Aplicativo externo assumiu o atendimento'
    case 'external_released':
      return 'Aplicativo externo liberou o atendimento'
    case 'protocol_opened':
      return `Protocolo ${ev.protocol ?? ''} aberto`.replace('  ', ' ')
    case 'protocol_closed':
      return `Protocolo ${ev.protocol ?? ''} encerrado`.replace('  ', ' ')
    case 'finalized': {
      const reason = FINALIZE_REASON[ev.detail ?? ''] ?? ''
      const by = ev.system ? ' automaticamente' : actor ? ` por ${actor}` : ''
      const tail = [ev.protocol ? `Protocolo ${ev.protocol}` : '', reason].filter(Boolean).join(' · ')
      return `Atendimento finalizado${by}${tail ? ` · ${tail}` : ''}`
    }
    default:
      return ''
  }
}

export type TimelineItem = { type: 'message'; at: string; message: MessageItem } | { type: 'event'; at: string; event: TimelineEvent }

/**
 * Messages and events in one chronological list. Messages page newest-first, so while older pages are still unloaded an event older than the oldest loaded
 * message is held back (otherwise it would float to the top with nothing around it); it shows once that history is loaded. A message wins a tie.
 */
export function mergeTimeline(messages: MessageItem[], events: TimelineEvent[], hasOlderMessages: boolean): TimelineItem[] {
  const oldest = messages.length > 0 ? Date.parse(messages[0].created_at) : Number.POSITIVE_INFINITY
  const shown = events.filter((e) => !hasOlderMessages || Date.parse(e.at) >= oldest)
  const items: TimelineItem[] = [
    ...messages.map<TimelineItem>((message) => ({ type: 'message', at: message.created_at, message })),
    ...shown.map<TimelineItem>((event) => ({ type: 'event', at: event.at, event })),
  ]
  // stable: messages were listed first, so on equal times a message stays before the event
  return items
    .map((item, i) => ({ item, i, t: Date.parse(item.at) }))
    .sort((a, b) => a.t - b.t || a.i - b.i)
    .map((x) => x.item)
}

/** Keeps only what this screen knows how to draw: a newer server may send a kind this build has no words for, and a line with no words is worse than none. */
export function knownEvents(raw: unknown): TimelineEvent[] {
  if (!Array.isArray(raw)) return []
  return raw.filter((e): e is TimelineEvent => !!e && typeof e.id === 'string' && typeof e.at === 'string' && KNOWN_KINDS.has(e.kind) && Number.isFinite(Date.parse(e.at)))
}
