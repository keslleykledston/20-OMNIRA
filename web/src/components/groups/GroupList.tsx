import clsx from 'clsx'
import { Badge } from '../primitives'
import type { Group } from '../../lib/groups'
import { GROUP_PURPOSE_BADGE_VARIANT, GROUP_PURPOSE_SHORT_LABEL, groupPreview } from '../../lib/groupModel'
import { inboxTimeLabel } from '../../lib/inboxModel'

interface Props {
  groups: Group[]
  selectedId: string | null
  onSelect: (id: string) => void
  isLoading: boolean
  /** Shown only when the tenant runs more than one WhatsApp instance — otherwise it is always the same, and noise. */
  showInstanceLabel?: boolean
}

// Compact rows like the Inbox, but a group has no wait, no assignee and no status: just who spoke last.
export function GroupList({ groups, selectedId, onSelect, isLoading, showInstanceLabel }: Props) {
  if (isLoading) return <div className="p-4 text-center text-sm text-text-secondary">Carregando...</div>
  return (
    <ul>
      {groups.map((g) => (
        <li key={g.id}>
          <button
            type="button"
            onClick={() => onSelect(g.id)}
            aria-current={selectedId === g.id ? 'true' : undefined}
            className={clsx(
              'flex w-full items-center gap-2.5 px-3 py-1.5 text-left border-b border-border-subtle transition-colors',
              selectedId === g.id ? 'bg-accent-primary-soft' : 'hover:bg-surface-muted',
            )}
          >
            <span className="flex h-8 w-8 flex-shrink-0 items-center justify-center rounded-full bg-surface-tertiary text-sm font-semibold text-text-secondary">
              {g.name?.[0]?.toUpperCase() || '#'}
            </span>
            <span className="min-w-0 flex-1">
              <span className="flex items-center gap-1.5">
                <span className="truncate text-sm font-semibold text-text-primary">{g.name || 'Grupo sem nome'}</span>
                <Badge variant={GROUP_PURPOSE_BADGE_VARIANT[g.purpose]} size="xs" className="flex-shrink-0">
                  {GROUP_PURPOSE_SHORT_LABEL[g.purpose]}
                </Badge>
                <time dateTime={g.last_message_at ?? undefined} className="ml-auto flex-shrink-0 text-[11px] tabular-nums text-text-tertiary">
                  {inboxTimeLabel(g.last_message_at ?? undefined)}
                </time>
              </span>
              <span className="block truncate text-xs text-text-secondary">
                {g.account_name && <span className="text-text-tertiary">{g.account_name} · </span>}
                {showInstanceLabel && g.connection_account && <span className="text-text-tertiary">{g.connection_account} · </span>}
                {groupPreview(g)}
              </span>
            </span>
          </button>
        </li>
      ))}
    </ul>
  )
}
