import clsx from 'clsx'
import { Card, Icon, Skeleton } from '../../../components/primitives'
import type { IconName } from '../../../components/primitives/Icon'
import type { Metric } from '../types'

const ACCENT: Record<Metric['id'], { icon: IconName; fg: string; bg: string }> = {
  active_conversations: { icon: 'conversations', fg: 'text-status-success', bg: 'bg-status-success-soft' },
  new_contacts: { icon: 'contacts', fg: 'text-status-info', bg: 'bg-status-info-soft' },
  open_tickets: { icon: 'tickets', fg: 'text-status-warning', bg: 'bg-status-warning-soft' },
  avg_first_response: { icon: 'clock', fg: 'text-accent-primary', bg: 'bg-accent-primary-soft' },
}

export function MetricCard({ metric, onSelect }: { metric: Metric; onSelect?: () => void }) {
  const accent = ACCENT[metric.id]
  const up = metric.trend.direction === 'up'

  return (
    <Card
      padding="compact"
      interactive={!!onSelect}
      onClick={onSelect}
      className={clsx('h-full', onSelect && 'focus-visible:ring-2 focus-visible:ring-accent-primary')}
      {...(onSelect ? { role: 'button', tabIndex: 0 } : {})}
    >
      <div className="flex items-center justify-between gap-3">
        <div className="min-w-0">
          <p className="text-body-sm text-text-secondary truncate">{metric.label}</p>
          <p className="mt-2 text-display-md font-bold text-text-primary">{metric.value}</p>
          <p
            className={clsx(
              'mt-2 flex items-center gap-1 text-caption',
              up ? 'text-status-success' : 'text-status-danger',
            )}
          >
            <Icon name={up ? 'arrow-up' : 'arrow-down'} size={14} />
            {metric.trend.percent}%
          </p>
        </div>
        <span className={clsx('flex-shrink-0 rounded-card p-2.5', accent.bg, accent.fg)}>
          <Icon name={accent.icon} size={22} />
        </span>
      </div>
    </Card>
  )
}

export function MetricCardSkeleton() {
  return (
    <Card padding="compact" className="h-full">
      <Skeleton width="w-24" height="h-4" />
      <div className="mt-3">
        <Skeleton width="w-16" height="h-8" />
      </div>
      <div className="mt-3">
        <Skeleton width="w-12" height="h-3" />
      </div>
    </Card>
  )
}
