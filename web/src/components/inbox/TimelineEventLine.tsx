import { Icon } from '../primitives'
import type { IconName } from '../primitives/Icon'
import { timelineText, type TimelineEvent, type TimelineEventKind } from '../../lib/timelineEvents'

const ICON: Record<TimelineEventKind, IconName> = {
  claimed: 'user-plus',
  assigned: 'user-plus',
  released: 'user-minus',
  transferred: 'transfer',
  sent_to_queue: 'transfer',
  taken_over: 'shield-check',
  auto_assigned: 'sparkles',
  external_claimed: 'plug',
  external_released: 'plug',
  protocol_opened: 'tickets',
  protocol_closed: 'tickets',
  finalized: 'check-circle',
}

/** One discreet centered line between the messages: what happened to the attendance (taken, handed over, protocol, finalized). */
export default function TimelineEventLine({ event, selfId }: { event: TimelineEvent; selfId: string | null }) {
  const time = new Date(event.at).toLocaleTimeString('pt-BR', { hour: '2-digit', minute: '2-digit' })
  return (
    <p data-event-kind={event.kind} className="my-1.5 flex items-center justify-center gap-1.5 px-6 text-center text-[11px] text-text-tertiary">
      <Icon name={ICON[event.kind]} size={12} />
      <span>{timelineText(event, selfId)}</span>
      <span aria-hidden="true">· {time}</span>
    </p>
  )
}
