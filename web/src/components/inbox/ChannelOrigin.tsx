import clsx from 'clsx'

// Where a conversation CAME FROM, as a small logo (owner decision 2026-10-09): the inbox no longer filters by channel, it merges every
// channel of an instance, so each row says its origin. Only WhatsApp exists today; e-mail, Instagram and Facebook are drawn already so
// a future integration only has to send its provider name. Never colour alone: the logo always carries a text name (title + aria-label).
export type ChannelKind = 'whatsapp' | 'email' | 'instagram' | 'facebook' | 'unknown'

const LABEL: Record<ChannelKind, string> = { whatsapp: 'WhatsApp', email: 'E-mail', instagram: 'Instagram', facebook: 'Facebook', unknown: 'Canal não informado' }

/** Maps a provider id (waha, meta_cloud, …) or a free channel name ("WhatsApp", "email") to the logo to draw. */
export function channelKind(source?: string | null): ChannelKind {
  const s = (source ?? '').toLowerCase()
  if (!s) return 'unknown'
  if (/whats|waha|meta_cloud|wa_/.test(s)) return 'whatsapp'
  if (/mail|smtp|imap/.test(s)) return 'email'
  if (/insta/.test(s)) return 'instagram'
  if (/face|messenger|fb_/.test(s)) return 'facebook'
  return 'unknown'
}

/** The readable name of a provider id or channel name ("waha" -> "WhatsApp"); an unknown but non-empty name is kept as written. */
export function channelLabel(source?: string | null): string {
  const kind = channelKind(source)
  return kind === 'unknown' && source ? source : LABEL[kind]
}

const TONE: Record<ChannelKind, string> = {
  whatsapp: 'bg-status-success-soft text-status-success',
  email: 'bg-surface-muted text-text-secondary',
  instagram: 'bg-status-danger-soft text-status-danger',
  facebook: 'bg-status-info-soft text-status-info',
  unknown: 'bg-surface-muted text-text-tertiary',
}

function Glyph({ kind }: { kind: ChannelKind }) {
  switch (kind) {
    case 'whatsapp':
      return <path d="M12 3a9 9 0 0 0-7.7 13.6L3 21l4.5-1.2A9 9 0 1 0 12 3Zm4.6 12.4c-.2.6-1.2 1.1-1.7 1.1-.4 0-1 .2-3.2-.7-2.7-1.1-4.4-3.8-4.5-4-.1-.1-1.1-1.4-1.1-2.7s.7-1.9.9-2.2c.2-.2.5-.3.7-.3h.5c.2 0 .4 0 .5.4l.8 1.9c.1.2.1.4 0 .5l-.3.5-.4.4c-.1.1-.3.3-.1.6.2.3.7 1.2 1.6 1.9 1.1 1 2 1.3 2.3 1.4.3.1.4.1.6-.1l.7-.9c.2-.3.4-.2.6-.1l1.8.9c.3.1.4.2.5.3 0 .2 0 .8-.2 1.3Z" fill="currentColor" stroke="none" />
    case 'email':
      return <path d="M4 6h16a1 1 0 0 1 1 1v10a1 1 0 0 1-1 1H4a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1Zm0 1 8 6 8-6" />
    case 'instagram':
      return <><rect x="4" y="4" width="16" height="16" rx="5" /><circle cx="12" cy="12" r="3.5" /><path d="M16.8 7.2h.01" /></>
    case 'facebook':
      return <path d="M14 8h2V5h-2.5A3.5 3.5 0 0 0 10 8.5V11H8v3h2v6h3v-6h2.3l.5-3H13V8.7c0-.4.3-.7.7-.7Z" fill="currentColor" stroke="none" />
    default:
      return <path d="M12 8v4m0 4h.01M21 12a9 9 0 1 1-18 0 9 9 0 0 1 18 0Z" />
  }
}

/** `source` is a provider id or a channel name; `detail` (e.g. the line label) is added to the tooltip only. */
export function ChannelOrigin({ source, detail, className }: { source?: string | null; detail?: string; className?: string }) {
  const kind = channelKind(source)
  const label = LABEL[kind]
  return (
    <span
      role="img"
      aria-label={label}
      title={detail ? `${label} · ${detail}` : label}
      className={clsx('inline-flex h-[18px] w-[18px] flex-shrink-0 items-center justify-center rounded-full', TONE[kind], className)}
    >
      <svg viewBox="0 0 24 24" width="12" height="12" fill="none" stroke="currentColor" strokeWidth="2" strokeLinecap="round" strokeLinejoin="round" aria-hidden="true" focusable="false">
        <Glyph kind={kind} />
      </svg>
    </span>
  )
}
