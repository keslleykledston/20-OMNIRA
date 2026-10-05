import clsx from 'clsx'
import { lineShortLabel, type ChannelLine } from '../../lib/channelLines'

/** Which line a conversation runs on. Official (Meta) is blue, the unofficial WhatsApp session is neutral. */
export function ChannelBadge({ line, className }: { line?: ChannelLine; className?: string }) {
  if (!line) return null
  const official = line.provider_kind === 'official'
  return (
    <span
      title={line.label + (line.can_send_text ? '' : ' (inativo)')}
      className={clsx(
        'inline-flex flex-shrink-0 items-center rounded-pill px-1.5 text-[10px] font-medium leading-4',
        official ? 'bg-status-info-soft text-status-info' : 'bg-surface-muted text-text-secondary',
        !line.can_send_text && 'opacity-60',
        className,
      )}
    >
      {lineShortLabel(line)}
    </span>
  )
}
