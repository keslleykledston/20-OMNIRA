import clsx from 'clsx';
import { Icon } from './Icon';
import type { IconName } from './Icon';
import { Skeleton } from './States';

export type MetricTone = 'neutral' | 'success' | 'warning' | 'danger' | 'info';

interface MetricCardProps {
  label: string;
  value: number | undefined;
  icon: IconName;
  tone?: MetricTone;
  helperText?: string;
}

const TONE_ICON_CLASS: Record<MetricTone, string> = {
  neutral: 'bg-accent-primary-soft text-accent-primary',
  success: 'bg-status-success-soft text-status-success',
  warning: 'bg-status-warning-soft text-status-warning',
  danger: 'bg-status-danger-soft text-status-danger',
  info: 'bg-status-info-soft text-status-info',
};

// Shared KPI card for operational overview surfaces (Dashboard today; any
// future page with real tenant-wide counts can reuse it). Value-only by
// design: no trend/change prop until a real historical/delta source exists
// (DESIGN.6-B gate — no fabricated trends).
export function MetricCard({ label, value, icon, tone = 'neutral', helperText }: MetricCardProps) {
  return (
    <div className="min-w-0 rounded-card border border-border-subtle bg-surface p-5 shadow-sm">
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0">
          <p className="text-body-sm text-text-secondary truncate">{label}</p>
          <p className="mt-2 text-display-md font-bold tabular-nums text-text-primary">
            {value ?? '—'}
          </p>
          {helperText && <p className="mt-1.5 text-xs text-text-tertiary">{helperText}</p>}
        </div>
        <span className={clsx('flex-shrink-0 rounded-card p-2.5', TONE_ICON_CLASS[tone])}>
          <Icon name={icon} size={20} />
        </span>
      </div>
    </div>
  );
}

export function MetricCardSkeleton() {
  return (
    <div className="min-w-0 rounded-card border border-border-subtle bg-surface p-5 shadow-sm">
      <Skeleton width="w-24" height="h-3.5" />
      <div className="mt-3">
        <Skeleton width="w-16" height="h-8" />
      </div>
    </div>
  );
}
