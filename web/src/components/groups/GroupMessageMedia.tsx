import { useState } from 'react'
import clsx from 'clsx'
import { Icon } from '../primitives'
import { groupMessageMediaUrl, type GroupMessage } from '../../lib/groups'

interface Props {
  tenantId: string
  groupId: string
  message: GroupMessage
}

// A group message's own image/PDF (ADR-0015 addendum 2) — only ever the cleared file; the server answers a
// status (pending/quarantined/infected/...) instead of bytes for anything else, surfaced here as <img>'s own
// onError (the same honest "couldn't load" the rest of the product already uses for media, nothing granular
// pulled in just for this). The vision-derived caption, when done, shows under it as what the IA read.
export function GroupMessageMedia({ tenantId, groupId, message }: Props) {
  const [failed, setFailed] = useState(false)
  if (message.message_type !== 'image' && message.message_type !== 'document') return null
  const url = groupMessageMediaUrl(tenantId, groupId, message.id)

  return (
    <div className={clsx('max-w-xs', message.body && 'mt-2')}>
      {message.message_type === 'image' && !failed && (
        <img src={url} alt="Imagem do grupo" className="h-auto max-w-full rounded" loading="lazy" onError={() => setFailed(true)} />
      )}
      {message.message_type === 'image' && failed && (
        <div role="status" className="flex items-center gap-1.5 rounded bg-surface-subtle px-2 py-1.5 text-xs text-text-secondary">
          <Icon name="info" size={14} />
          <span>Não foi possível carregar esta imagem.</span>
        </div>
      )}
      {message.message_type === 'document' && (
        <a
          href={url}
          target="_blank"
          rel="noreferrer"
          className="flex items-center gap-2 rounded border border-border-subtle bg-surface-subtle p-2 text-xs no-underline hover:bg-surface-hover"
        >
          <span aria-hidden="true" className="text-lg">📎</span>
          <span className="text-text-primary">Abrir documento</span>
        </a>
      )}
      {message.image_caption && (
        <div className="mt-1.5 border-l-2 border-border-subtle pl-2">
          <div className="mb-0.5 text-[11px] font-medium uppercase tracking-wide text-text-tertiary">O que a IA leu na imagem</div>
          <p className="whitespace-pre-wrap break-words text-xs text-text-secondary">{message.image_caption}</p>
        </div>
      )}
    </div>
  )
}
