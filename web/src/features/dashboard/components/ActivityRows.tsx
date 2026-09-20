import clsx from 'clsx'
import { Avatar, Badge } from '../../../components/primitives'
import type { RecentConversation, PriorityTicket } from '../types'
import {
  CHANNEL_COLOR,
  PRIORITY_LABEL,
  PRIORITY_VARIANT,
  TICKET_STATUS_LABEL,
  TICKET_STATUS_VARIANT,
} from './palette'

const row = 'flex items-center gap-3 py-3 text-left w-full transition-colors rounded-control focus-visible:ring-2 focus-visible:ring-accent-primary'

export function ConversationRow({
  conversation,
  onOpen,
}: {
  conversation: RecentConversation
  onOpen?: () => void
}) {
  return (
    <button type="button" onClick={onOpen} className={clsx(row, 'hover:bg-surface-muted px-2 -mx-2')}>
      <span className="relative flex-shrink-0">
        <Avatar alt={conversation.contact} size="md" />
        <span
          aria-hidden="true"
          className="absolute -bottom-0.5 -right-0.5 h-3 w-3 rounded-full ring-2 ring-surface"
          style={{ backgroundColor: CHANNEL_COLOR[conversation.channel] }}
        />
      </span>

      <span className="min-w-0 flex-1">
        <span className="block truncate text-body-sm font-semibold text-text-primary">
          {conversation.contact}
        </span>
        <span className="block truncate text-body-sm text-text-secondary">{conversation.preview}</span>
      </span>

      <span className="flex flex-shrink-0 flex-col items-end gap-1">
        <span className="text-metadata">{conversation.time}</span>
        {conversation.unreadCount > 0 && (
          <span className="flex h-5 min-w-5 items-center justify-center rounded-full bg-accent-primary px-1.5 text-metadata font-semibold text-white">
            {conversation.unreadCount}
          </span>
        )}
      </span>
    </button>
  )
}

export function PriorityTicketRow({
  ticket,
  onOpen,
}: {
  ticket: PriorityTicket
  onOpen?: () => void
}) {
  return (
    <button type="button" onClick={onOpen} className={clsx(row, 'hover:bg-surface-muted px-2 -mx-2 items-start')}>
      <span className="w-14 flex-shrink-0 pt-0.5 text-body-sm font-semibold text-text-primary">
        {ticket.id}
      </span>

      <span className="flex-shrink-0 pt-0.5">
        <Badge variant={PRIORITY_VARIANT[ticket.priority]} size="sm">
          <span aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-current" />
          {PRIORITY_LABEL[ticket.priority]}
        </Badge>
      </span>

      <span className="min-w-0 flex-1">
        <span className="block truncate text-body-sm font-semibold text-text-primary">
          {ticket.subject}
        </span>
        <span className="block truncate text-metadata">
          {ticket.contact} • {ticket.time}
        </span>
      </span>

      <span className="flex-shrink-0 pt-0.5">
        <Badge variant={TICKET_STATUS_VARIANT[ticket.status]} size="sm">
          {TICKET_STATUS_LABEL[ticket.status]}
        </Badge>
      </span>
    </button>
  )
}
